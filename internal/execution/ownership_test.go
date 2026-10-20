package execution

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"testing"
)

const ownershipTransaction = "transaction"

func ownershipError(e *Engine, op *session.Operation, stats sql.DBStats) error {
	if stats.OpenConnections != 1 || stats.InUse != 0 || stats.Idle != 1 {
		return fmt.Errorf("benchmark leaked a pool connection lease")
	}
	if e.physical.Closed() || e.control.ActiveTransaction() != nil || op.ResponseActive() || len(op.Resources(true)) != 0 {
		return fmt.Errorf("benchmark leaked a connection lease, transaction, response or resource")
	}
	return nil
}
func TestDriverResourceGuard(t *testing.T) {
	for _, name := range []string{"clean", "lease", ownershipTransaction, "unread-response", "resource", "closed"} {
		t.Run(name, func(t *testing.T) {
			ctx, e := protocolEngine(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if name == ownershipTransaction {
					testpeer.ServeOK(ctx, t, p, []byte("\x03START TRANSACTION READ WRITE"), 3)
				}
			})
			if name == ownershipTransaction {
				if _, err := e.BeginTx(ctx, driver.TxOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			op, err := e.physical.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer op.Release()
			stats := sql.DBStats{OpenConnections: 1, Idle: 1}
			switch name {
			case "lease":
				stats.InUse, stats.Idle = 1, 0
			case "unread-response":
				if err = op.BeginResponse(); err != nil {
					t.Fatal(err)
				}
			case "resource":
				if _, err = op.Register(session.CursorResource); err != nil {
					t.Fatal(err)
				}
			case "closed":
				e.Discard()
			}
			if err = ownershipError(e, op, stats); (err == nil) != (name == "clean") {
				t.Fatal("resource guard did not detect injected ownership violation:", err)
			}
		})
	}
}
