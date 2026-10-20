package connect_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestJavaParityCharsetHandshakeAndInitialization(t *testing.T) {
	for _, tc := range []struct {
		name, text, data string
		collation        byte
	}{
		{"HKSCS", "镬😀", "镬😀", 152}, {"HKSCS31", "镬😀", "镬😀", 153}, {"GB18030_2022", "龴", "\xfe\x59", 217},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := oceanbase.ParseDSN("oceanbase://user@localhost/?charset=" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Session.SkipVersionProbe = true
			cfg.User = tc.text
			cfg.Database = tc.text
			cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Native, false)) {
					return
				}
				response, readErr := p.ReadMessage(ctx)
				if readErr != nil || len(response) < 32 || response[8] != tc.collation || !bytes.HasPrefix(response[32:], []byte(tc.data+"\x00\x00"+tc.data+"\x00")) {
					t.Errorf("handshake charset or identity: %x %v", response, readErr)
					return
				}
				if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 2)) {
					return
				}
				for _, q := range []string{"SET NAMES " + strings.ToLower(tc.name), "SET autocommit=1", transactionUTCZone} {
					if !testpeer.ServeOK(ctx, t, p, append([]byte{3}, q...), 2) {
						return
					}
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			created, err := oceanbase.NewConnector(cfg)
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
