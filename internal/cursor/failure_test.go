package cursor

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestManagerFetchRetirement(t *testing.T) {
	ctx, op := cursorOperation(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !preparePeer(ctx, t, p, cursorQuery, 55) || !openPeer(ctx, t, p, 55) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, []byte{28, 55, 0, 0, 0, 1, 0, 0, 0}) || !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 66, 0}) {
			return
		}
		if testpeer.ReadCommand(ctx, t, p, []byte{25, 55, 0, 0, 0}) {
			testpeer.ServeOK(ctx, t, p, []byte{14}, 2)
		}
	})
	var manager Manager
	ref, err := manager.Open(ctx, op, nil, cursorQuery, nil, Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = op.Publish(ref); err != nil {
		t.Fatal(err)
	}
	if _, retire, err := manager.Fetch(ctx, op, nil, ref, Position{Origin: Absolute, Offset: 1}, 1, nil); retire || !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal("local positioning failure retired cursor", retire, err)
	}
	if _, retire, err := manager.Fetch(ctx, op, nil, ref, Position{}, 1, nil); !retire || !errors.Is(err, dberrors.ErrCursor) || op.Closed() {
		t.Fatal("empty unfinished batch did not retire only the cursor", retire, err)
	}
	closeOwned(ctx, t, op, &manager, ref)
	if _, err = command.ExecOK(ctx, op, []byte{14}); err != nil {
		t.Fatal("fetch failure lost response boundary", err)
	}
}

func mutationOwner(ctx context.Context, t *testing.T, op *session.Operation) (*Manager, session.ResourceRef) {
	t.Helper()
	manager := &Manager{}
	parent := statement.Adopt(op, 77, sqltext.Plan{}, nil)
	ref, err := manager.own(op, &record{statement: parent, scope: op.StatementScope(), columns: []wire.Column{{Name: "id"}}, identity: &catalog.CursorIdentity{Table: "`s`.`t`", Keys: []string{"id"}, Columns: []string{"id"}, Locking: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err = parent.Close(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = op.Publish(ref); err != nil {
		t.Fatal(err)
	}
	return manager, ref
}

func prepareMutationPeer(ctx context.Context, t *testing.T, p *wire.PacketIO, query string) bool {
	t.Helper()
	if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, query...)) {
		return false
	}
	return testpeer.SendPackets(ctx, t, p, []byte{0, 61, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 2, 0})
}

func mutationExecutePeer(ctx context.Context, t *testing.T, p *wire.PacketIO) bool {
	t.Helper()
	payload := []byte{23, 61, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 8, 0}
	return testpeer.ReadCommand(ctx, t, p, binary.LittleEndian.AppendUint64(payload, 7))
}

func TestManagerMutationRetirement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		affected byte
		warnings byte
		retire   bool
	}{{"success", 1, 0, false}, {"warning", 1, 1, true}, {"multiple rows", 2, 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, op := cursorOperation(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !prepareMutationPeer(ctx, t, p, "INSERT INTO `s`.`t` (`id`) VALUES (?)") || !mutationExecutePeer(ctx, t, p) || !testpeer.SendPackets(ctx, t, p, []byte{0, tc.affected, 0, 2, 0, tc.warnings, 0}) {
					return
				}
				if testpeer.ReadCommand(ctx, t, p, []byte{25, 61, 0, 0, 0}) {
					testpeer.ReadCommand(ctx, t, p, []byte{25, 77, 0, 0, 0})
				}
			})
			manager, ref := mutationOwner(ctx, t, op)
			if _, retire, err := manager.Mutate(ctx, op, nil, ref, Request{Operation: Insert, Changes: []ColumnChange{{Name: "missing", Value: int64(7)}}}, true, nil); retire || !errors.Is(err, dberrors.ErrCursor) {
				t.Fatal("local change validation retired cursor", retire, err)
			}
			result, retire, err := manager.Mutate(ctx, op, nil, ref, Request{Operation: Insert, Changes: []ColumnChange{{Name: "id", Value: int64(7)}}}, true, nil)
			if retire != tc.retire || (err != nil) != tc.retire || !tc.retire && result.Affected != 1 || op.Closed() {
				t.Fatal(result, retire, err)
			}
			closeOwned(ctx, t, op, manager, ref)
		})
	}
}

func TestManagerMissingRefreshRetires(t *testing.T) {
	ctx, op := cursorOperation(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !prepareMutationPeer(ctx, t, p, "SELECT `id` AS `id` FROM `s`.`t` WHERE `id` = ?") || !mutationExecutePeer(ctx, t, p) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if testpeer.ReadCommand(ctx, t, p, []byte{25, 61, 0, 0, 0}) {
			testpeer.ReadCommand(ctx, t, p, []byte{25, 77, 0, 0, 0})
		}
	})
	manager, ref := mutationOwner(ctx, t, op)
	if _, retire, err := manager.Refresh(ctx, op, nil, ref, Request{Operation: Refresh, Key: map[string]any{"id": int64(7)}}, true, nil); !retire || !errors.Is(err, sql.ErrNoRows) || op.Closed() {
		t.Fatal(retire, err)
	}
	closeOwned(ctx, t, op, manager, ref)
}
