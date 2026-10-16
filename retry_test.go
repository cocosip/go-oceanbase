package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/testutil"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

type partialCommandConn struct {
	net.Conn
	commands *atomic.Int32
}

func (c partialCommandConn) Write(data []byte) (int, error) {
	if bytes.Equal(data, []byte("\x03UPDATE once")) {
		c.commands.Add(1)
		n, err := c.Conn.Write(data[:1])
		if err != nil {
			return n, err
		}
		return n, testutil.ErrInjected
	}
	return c.Conn.Write(data)
}

func TestNoReplayAfterSend(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost-response", true: "partial-write"}[partial], func(t *testing.T) {
			var dials, commands atomic.Int32
			dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				if partial {
					p.ResetSequence()
					if _, err := p.ReadMessage(context.Background()); err == nil {
						t.Error("partial write became complete command")
					}
					return
				}
				if readQuery(context.Background(), t, p, "UPDATE once") {
					commands.Add(1)
				}
			})
			c, err := NewConnector(Config{User: testUser, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials.Add(1)
				raw, err := dial(ctx, network, addr)
				if partial && err == nil {
					raw = partialCommandConn{raw, &commands}
				}
				return raw, err
			}})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(c)
			defer func() { _ = db.Close() }()
			_, err = db.ExecContext(context.Background(), "UPDATE once")
			var transport *wire.IOError
			if err == nil || errors.Is(err, driver.ErrBadConn) || !errors.As(err, &transport) {
				t.Fatal("unsafe replay classification", err)
			}
			if partial && (!errors.Is(err, testutil.ErrInjected) || transport.Bytes < 5) {
				t.Fatal("lost transfer evidence", err)
			}
			if commands.Load() != 1 || dials.Load() != 1 || db.Stats().Idle != 0 {
				t.Fatalf("commands=%d dials=%d idle=%d", commands.Load(), dials.Load(), db.Stats().Idle)
			}
		})
	}
}
