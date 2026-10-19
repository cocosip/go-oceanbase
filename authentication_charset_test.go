package oceanbase

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const authPasswordCharsetTest = "LATIN1"

func TestAuthPluginPasswordCharset(t *testing.T) {
	for _, name := range []string{authNative, authEd25519, authPAM, authClear} {
		c, err := NewConnector(Config{User: testUser, Auth: AuthOptions{PasswordCharset: authPasswordCharsetTest}})
		if err != nil {
			t.Fatal(err)
		}
		plugin, err := c.(*connector).newAuthPlugin(name)
		if err != nil {
			t.Fatal(err)
		}
		challenge := AuthChallenge{Nonce: []byte("12345678901234567890"), TLSVerified: true}
		var want []byte
		switch name {
		case authNative:
			want, _ = auth.Native("\xe9", challenge.Nonce)
		case authEd25519:
			challenge.Nonce = make([]byte, 32)
			want, _ = auth.Ed25519("\xe9", challenge.Nonce)
		case authPAM:
			challenge.Nonce = []byte("\x04Password: ")
			want = []byte{0xe9, 0}
		case authClear:
			want = []byte{0xe9, 0}
		}
		got, err := authResponse(context.Background(), plugin, name, Credentials{Password: "é"}, challenge)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s response: %x %v", name, got, err)
		}
	}
	if _, err := NewConnector(Config{User: testUser, Auth: AuthOptions{PasswordCharset: "no-such-charset"}}); err == nil {
		t.Fatal("invalid password charset accepted")
	}
}

func TestAuthPluginOptionsDSN(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://user@localhost/?allowOldPassword=true&passwordCharset=latin1")
	if err != nil || !cfg.Auth.AllowOldPassword || !strings.EqualFold(cfg.Auth.PasswordCharset, authPasswordCharsetTest) {
		t.Fatalf("auth DSN: %+v %v", cfg.Auth, err)
	}
	for _, value := range []string{"1", "TRUE", "false&allowOldPassword=true"} {
		if _, err = ParseDSN("oceanbase://user@localhost/?allowOldPassword=" + value); err == nil {
			t.Fatal("invalid bool accepted")
		}
	}
	_, err = ParseDSN("oceanbase://user@localhost/?passwordCharset=unknown")
	var config *ConfigError
	if !errors.As(err, &config) {
		t.Fatal("invalid charset", err)
	}
}

func TestJavaParityCharsetHandshakeAndInitialization(t *testing.T) {
	for _, tc := range []struct {
		name, text, data string
		collation        byte
	}{
		{"HKSCS", "镬😀", "镬😀", 152}, {"HKSCS31", "镬😀", "镬😀", 153}, {"GB18030_2022", "龴", "\xfe\x59", 217},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseDSN("oceanbase://user@localhost/?charset=" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Session.SkipVersionProbe = true
			cfg.User = tc.text
			cfg.Database = tc.text
			cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !sendPackets(ctx, t, p, authGreeting(authNative, false)) {
					return
				}
				response, readErr := p.ReadMessage(ctx)
				if readErr != nil || len(response) < 32 || response[8] != tc.collation || !bytes.HasPrefix(response[32:], []byte(tc.data+"\x00\x00"+tc.data+"\x00")) {
					t.Errorf("handshake charset or identity: %x %v", response, readErr)
					return
				}
				if !sendPackets(ctx, t, p, commandPacket(0, 0, 2)) {
					return
				}
				for _, q := range []string{"SET NAMES " + strings.ToLower(tc.name), "SET autocommit=1", transactionUTCZone} {
					if !serveOK(ctx, t, p, append([]byte{3}, q...), 2) {
						return
					}
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			created, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			connection, err := created.Connect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = connection.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJavaParityCharsetSQLParametersAndResults(t *testing.T) {
	for _, tc := range []struct {
		name, text, data string
		collation        uint16
	}{
		{"hkscs", "镬😀", "镬😀", 152}, {"hkscs31", "镬😀", "镬😀", 153}, {"gb18030_2022", "龴\ufe10\ue000", "\xfe\x59\xa6\xd9\xaa\xa1", 217},
	} {
		for _, mode := range []Mode{ModeMySQL, ModeOracle} {
			c := &conn{cfg: Config{Session: SessionOptions{Charset: tc.name}, Limits: Limits{MaxMessageBytes: 4096}}, session: &handshakeSession{mode: mode}}
			sql, err := c.encodeSQL("SELECT '" + tc.text + "'")
			if err != nil || string(sql) != "SELECT '"+tc.data+"'" {
				t.Fatal(tc.name, sql, err)
			}
			p, err := encodeParameter(tc.text, mode, c.cfg.Session, 4096)
			if err != nil || string(p.Data) != tc.data {
				t.Fatal(tc.name, p, err)
			}
			literal, err := c.clientLiteral(tc.text, 4096)
			if err != nil {
				t.Fatal(tc.name, err)
			}
			sql, err = c.encodeSQL(literal)
			if err != nil {
				t.Fatal(err)
			}
			if mode == ModeOracle && string(sql) != "'"+tc.data+"'" {
				t.Fatal("client bind", tc.name, sql)
			}
			if mode == ModeMySQL && literal != "_"+tc.name+" X'"+hex.EncodeToString([]byte(tc.data))+"'" {
				t.Fatal("client bind", tc.name, literal)
			}
			for _, binaryRows := range []bool{false, true} {
				v, err := c.decodeRowValue(wire.Column{Type: 253, Charset: tc.collation}, binaryRows, []byte(tc.data))
				if err != nil || v != tc.text {
					t.Fatal(tc.name, v, err)
				}
			}
			if mode == ModeOracle {
				c.cfg.Session.NCharset = tc.name
				national, nationalErr := encodeParameter(NString(tc.text), mode, c.cfg.Session, 4096)
				if nationalErr != nil || string(national.Data) != tc.data {
					t.Fatal("national parameter", tc.name, national, nationalErr)
				}
				v, nationalErr := c.decodeRowValue(wire.Column{Type: 208, Charset: 63}, true, []byte(tc.data))
				if nationalErr != nil || v != tc.text {
					t.Fatal("national result", tc.name, v, nationalErr)
				}
			}
		}
	}
}
