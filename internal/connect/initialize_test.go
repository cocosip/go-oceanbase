package connect

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestInitializeVariableLiteral(t *testing.T) {
	for _, tc := range []struct {
		name, value, literal string
		mode                 config.Mode
		status               uint16
		typ                  byte
		probe                bool
	}{
		{"signed", "+00600", "600", config.ModeMySQL, 2, 8, true},
		{"unsigned", "18446744073709551615", "18446744073709551615", config.ModeMySQL, 2, 8, true},
		{"numeric-string", "000600", "'000600'", config.ModeMySQL, 2, 253, true},
		{"ordinary-string", "x\\'y", "'x\\\\''y'", config.ModeMySQL, 2, 253, false},
		{"no-backslash", "x\\'y", testSessionQuotedNoBackslash, config.ModeMySQL, 514, 253, false},
		{"oracle-string", "000600", "'000600'", config.ModeOracle, 6, 253, true},
		{"oracle-integer", "-00600", "-600", config.ModeOracle, 6, 8, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Normalize(config.Config{User: testUser, Mode: tc.mode, Session: config.SessionOptions{SkipVersionProbe: true, Variables: map[string]string{"wait_timeout": tc.value}}})
			if err != nil {
				t.Fatal(err)
			}
			client, server := net.Pipe()
			t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				p := wire.NewPacketIO(server, cfg.Limits.MaxMessageBytes)
				ok := func(query string) bool {
					if !testpeer.ReadQuery(ctx, t, p, query) {
						return false
					}
					return testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, byte(tc.status), byte(tc.status >> 8), 0, 0})
				}
				if !ok("SET NAMES utf8mb4") || !ok("SET autocommit=1") || !ok("SET SESSION time_zone = '+00:00'") {
					return
				}
				if tc.probe {
					query := "SELECT @@SESSION.wait_timeout"
					if tc.mode == config.ModeOracle {
						query += sessionReadDualSuffix
					}
					if !testpeer.ReadQuery(ctx, t, p, query) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("value", tc.typ, 45, 0), []byte{254, 0, 0, byte(tc.status), byte(tc.status >> 8)}, []byte{1, '1'}, []byte{254, 0, 0, byte(tc.status), byte(tc.status >> 8)}) {
						return
					}
				}
				if !ok("SET SESSION wait_timeout = "+tc.literal) || !ok("SET NAMES utf8mb4") || !ok("SET SESSION time_zone = '+00:00'") {
					return
				}
			}()
			physical := session.New(cfg, &session.Negotiated{Conn: client, Raw: client, Packets: wire.NewPacketIO(client, cfg.Limits.MaxMessageBytes), Mode: tc.mode, Capabilities: wire.ClientProtocol41, OK: wire.OK{Status: tc.status}})
			defer physical.Discard()
			op, err := physical.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = Initialize(ctx, op, cfg)
			op.Release()
			if err != nil {
				t.Fatal(err)
			}
			<-done
		})
	}
}
