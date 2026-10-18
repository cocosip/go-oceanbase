package oceanbase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	identityPrivate      = "private-identity"
	identityUser         = "测试@tenant"
	identityGBKDatabase  = "\xd3\xa6\xd3\xc3"
	identityCharsetLatin = "latin1"
	identityCharsetASCII = "ascii"
)

func TestHandshakeIdentityEncoding(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, tc := range []struct {
			charset, user, database, encodedUser, encodedDatabase string
			collation                                             byte
		}{
			{charsetUTF8MB4, identityUser, "应用", identityUser, "应用", 45},
			{charsetUTF8, identityUser, "应用", identityUser, "应用", 33},
			{localFilenameCharset, identityUser, "应用", testAttributeGBKValue + "@tenant", identityGBKDatabase, 28},
			{testAttributeGB18030, "\u0080@tenant", "\u0080", "\x81\x30\x81\x30@tenant", "\x81\x30\x81\x30", 248},
			{identityCharsetLatin, "é@tenant", "café", "\xe9@tenant", "caf\xe9", 8},
		} {
			for _, dynamic := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/callback-%t", mode, tc.charset, dynamic), func(t *testing.T) {
					calls := 0
					cfg := Config{User: tc.user, Database: tc.database, Mode: mode, Session: SessionOptions{Charset: tc.charset}}
					if dynamic {
						cfg.User = testUser
						cfg.Credentials = func(context.Context) (Credentials, error) {
							calls++
							return Credentials{User: tc.user}, nil
						}
					}
					session, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
						if !sendPackets(ctx, t, p, authGreeting(authNative, false)) {
							return
						}
						got, err := p.ReadMessage(ctx)
						want := []byte(tc.encodedUser + "\x00\x00" + tc.encodedDatabase + "\x00")
						if err != nil || len(got) < 32 || got[8] != tc.collation || !bytes.HasPrefix(got[32:], want) {
							t.Error("authentication identity does not match declared handshake charset", err)
						}
						status := byte(2)
						if mode == ModeOracle {
							status = 6
						}
						sendPackets(ctx, t, p, commandPacket(0, 0, status))
					})
					if err != nil || session == nil || session.mode != mode || session.user != tc.user || calls != map[bool]int{false: 0, true: 1}[dynamic] {
						t.Fatal("authentication changed the logical identity or callback count", err)
					}
				})
			}
		}
	}
}

func TestHandshakeIdentityEncodingRejected(t *testing.T) {
	for _, field := range []string{"Credentials.User", configDatabaseField} {
		for _, tc := range []struct{ name, charset, value string }{
			{"invalid-utf8", charsetUTF8MB4, identityPrivate + "\xff"},
			{identityCharsetASCII, identityCharsetASCII, identityPrivate + "测"},
			{localFilenameCharset, localFilenameCharset, identityPrivate + "😀"},
			{"utf8mb3", charsetUTF8, identityPrivate + "😀"},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				pluginCalls := 0
				cfg := Config{User: testUser, Database: "example", Session: SessionOptions{Charset: tc.charset}, Auth: AuthOptions{Plugins: map[string]AuthFactory{
					authNative: func() AuthPlugin { pluginCalls++; return builtinAuth{name: authNative, charset: charsetUTF8MB4} },
				}}}
				if field == configDatabaseField {
					cfg.Database = tc.value
				} else {
					cfg.Credentials = func(context.Context) (Credentials, error) { return Credentials{User: tc.value}, nil }
				}
				var written int
				session, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
					if !sendPackets(ctx, t, p, authGreeting(authNative, false)) {
						return
					}
					data, readErr := p.ReadMessage(ctx)
					written = len(data)
					if readErr == nil {
						sendPackets(ctx, t, p, []byte{255, 21, 4, '#', '2', '8', '0', '0', '0'})
					}
				})
				var configErr *ConfigError
				if session != nil || !errors.As(err, &configErr) || configErr.Field != field || written != 0 || pluginCalls != 0 || strings.Contains(err.Error(), identityPrivate) {
					t.Fatalf("invalid identity reached authentication: error=%v bytes=%d plugins=%d", err, written, pluginCalls)
				}
			})
		}
	}
}
