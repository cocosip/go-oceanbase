package materialize

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestFetchErrorResponseBoundary(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/malformed=%v", mode, malformed), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				t.Cleanup(cancel)
				client, peer := net.Pipe()
				cfg, err := config.Normalize(config.Config{User: "app", Mode: mode})
				if err != nil {
					t.Fatal(err)
				}
				s := session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: wire.NewPacketIO(client, 0), Mode: mode})
				op, err := s.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer op.Release()
				done := make(chan struct{})
				t.Cleanup(func() { _ = client.Close(); _ = peer.Close(); <-done })
				go func() {
					defer close(done)
					p := wire.NewPacketIO(peer, 0)
					packet := []byte{255, 0x15, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}
					if malformed {
						packet = []byte{255}
					}
					if !testpeer.ReadCommand(ctx, t, p, []byte{28, 7, 0, 0, 0, 1, 0, 0, 0}) || !testpeer.SendPackets(ctx, t, p, packet) || malformed {
						return
					}
					testpeer.ServeOK(ctx, t, p, []byte{14}, 2)
				}()
				_, _, _, err = New(op, nil).Fetch(ctx, 7, 1, []wire.Column{{Type: 8}}, command.FetchOptions{})
				if malformed {
					if !errors.Is(err, wire.ErrMalformed) || !op.Closed() {
						t.Fatalf("malformed FETCH retained session: closed=%v err=%v", op.Closed(), err)
					}
					return
				}
				var server *dberrors.Error
				if !errors.As(err, &server) || server.Code != 1045 || server.SQLState != "42000" || op.Closed() || op.ResponseActive() {
					t.Fatalf("server FETCH error lost boundary: closed=%v active=%v err=%v", op.Closed(), op.ResponseActive(), err)
				}
				if _, err = command.ExecOK(ctx, op, []byte{14}); err != nil {
					t.Fatal("FETCH error prevented subsequent PING", err)
				}
			})
		}
	}
}
