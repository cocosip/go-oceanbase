package cursor

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// TestSensitiveRefreshPreparesOnce pins one COM_STMT_PREPARE for a whole refreshed batch.
// A second prepare would be read where an execute is expected and fail the assertion.
func TestSensitiveRefreshPreparesOnce(t *testing.T) {
	const query = `SELECT "id" FROM "s"."t" WHERE ROWID = ?`
	ctx, op := cursorOperation(t, config.CompatibilityModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !prepareMutationPeer(ctx, t, p, query) {
			return
		}
		for _, value := range []byte{'a', 'b'} {
			p.ResetSequence()
			command, err := p.ReadMessage(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			if len(command) == 0 || command[0] != 23 {
				t.Errorf("expected one prepare followed by executes, got command %x", command)
				return
			}
			if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 253, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 0, 1, value}, []byte{254, 0, 0, 2, 0}) {
				return
			}
		}
		// COM_STMT_CLOSE has no response packet.
		testpeer.ReadCommand(ctx, t, p, []byte{25, 61, 0, 0, 0})
	})
	manager := &Manager{}
	parent := statement.Adopt(op, 77, sqltext.Plan{}, nil)
	columns := []wire.Column{
		{Schema: "s", OriginalTable: "t", OriginalName: "id", Name: "id", Type: 253, Charset: 63},
		{Name: "ROWID", Type: 253, Charset: 63},
	}
	ref, err := manager.own(op, &record{statement: parent, scope: op.StatementScope(), columns: columns, options: Options{Mode: Sensitive},
		identity: &catalog.CursorIdentity{Table: `"s"."t"`, Keys: []string{"id"}, Columns: []string{"id"}, Locking: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err = parent.Close(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = op.Publish(ref); err != nil {
		t.Fatal(err)
	}
	o, err := manager.operation(op, nil, ref)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := o.refreshSensitive(ctx, [][]driver.Value{{nil, []byte("AAAA")}, {nil, []byte("BBBB")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || catalog.Text(rows[0][0]) != "a" || catalog.Text(rows[1][0]) != "b" {
		t.Fatalf("refreshed rows: %v", rows)
	}
}
