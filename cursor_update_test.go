package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func cursorBaseColumn(name string, typ byte) []byte {
	b := []byte{}
	for _, v := range []string{"def", "s", "t", "t", name, name} {
		b = append(b, byte(len(v)))
		b = append(b, v...)
	}
	return append(b, 12, 63, 0, 64, 0, 0, 0, typ, 0, 0, 0, 0, 0)
}

func TestCursorUpdateIdentity(t *testing.T) {
	const query = "SELECT id,b FROM s.t"
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !servePrepare(ctx, t, p, query, 56, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(56)) || !sendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 67, 0}) || !serveTableDictionary(ctx, t, p, 3, tableDictionary()...) {
			return
		}
		for _, op := range []string{"UPDATE", "DELETE"} {
			query := "UPDATE `s`.`t` SET `b` = ? WHERE `id` = ?"
			count, affected := 2, byte(0)
			execute := append(executeHeader(61), 0, 1, 252, 0, 8, 0, 9)
			execute = append(execute, []byte("unchanged")...)
			execute = binary.LittleEndian.AppendUint64(execute, 7)
			if op == "DELETE" {
				query = "DELETE FROM `s`.`t` WHERE `id` = ?"
				count, affected = 1, 1
				execute = append(executeHeader(61), 0, 1, 8, 0, 7, 0, 0, 0, 0, 0, 0, 0)
			}
			if !servePrepareStatus(ctx, t, p, query, 61, count, nil, 3) || !serveTableRows(ctx, t, p, 3, []string{"SELECT 1 FROM `s`.`t` WHERE `id` =  7  FOR UPDATE"}, []byte{8}, []any{"1"}) || !readCommand(ctx, t, p, execute) || !sendPackets(ctx, t, p, []byte{0, affected, 0, 3, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 61)) {
				return
			}
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 56)) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tr, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	cur, err := OpenCursor(context.Background(), tr, query, CursorOptions{Updatable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.Update(context.Background(), map[string]any{"id": 7}, ColumnChange{Name: "b", Value: []byte("unchanged")}); err != nil {
		t.Fatal("no-op update was treated as deletion", err)
	}
	if err = cur.Delete(context.Background(), map[string]any{"id": 7}); err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorUpdateShape(t *testing.T) {
	for _, query := range []string{"SELECT t.id FROM t JOIN u ON t.id=u.id", "SELECT COUNT(*) FROM t", "SELECT id FROM t,u", "SELECT DISTINCT id FROM t", "SELECT id FROM t UNION SELECT id FROM t", "SELECT id FROM (SELECT id FROM t) x"} {
		if err := cursorUpdatableQuery(query, ModeMySQL, false); !errors.Is(err, ErrCursor) {
			t.Fatal(query, err)
		}
	}
	cols := []wire.Column{{Schema: "s", OriginalTable: "t", OriginalName: "b", Name: "b", Type: 252}}
	dict := lobRowset{columns: make([]wire.Column, 8), rows: [][]driver.Value{{"s", "t", blobBaseTable, blobLockingEngine, "id", "bigint", "", int64(1)}, {"s", "t", blobBaseTable, blobLockingEngine, "b", "LONGBLOB", "", nil}}}
	if _, err := resolveCursorIdentity(ModeMySQL, cols, []lobRowset{dict}); !errors.Is(err, ErrCursor) {
		t.Fatal("missing projected primary key accepted", err)
	}
}

func TestCursorInsertRefresh(t *testing.T) {
	const query = "SELECT id,b FROM s.t"
	const refresh = "SELECT `id` AS `id`,`b` AS `b` FROM `s`.`t` WHERE `id` = ?"
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !servePrepare(ctx, t, p, query, 57, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(57)) || !sendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 67, 0}) || !serveTableDictionary(ctx, t, p, 3, tableDictionary()...) {
			return
		}
		if !servePrepareStatus(ctx, t, p, "INSERT INTO `s`.`t` (`id`,`b`) VALUES (?,?)", 62, 2, nil, 3) {
			return
		}
		insert := append(executeHeader(62), 0, 1, 8, 0, 252, 0, 7, 0, 0, 0, 0, 0, 0, 0, 3, 'n', 'e', 'w')
		if !readCommand(ctx, t, p, insert) || !sendPackets(ctx, t, p, []byte{0, 1, 0, 3, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 62)) || !servePrepareStatus(ctx, t, p, refresh, 58, 1, nil, 3) {
			return
		}
		execute := append(executeHeader(58), 0, 1, 8, 0, 7, 0, 0, 0, 0, 0, 0, 0)
		if !readCommand(ctx, t, p, execute) || !sendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 3, 0}, append(binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), 3, 'n', 'e', 'w'), []byte{254, 0, 0, 3, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 58)) || !readCommand(ctx, t, p, stmtPacket(25, 57)) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tr, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	cur, err := OpenCursor(context.Background(), tr, query, CursorOptions{Updatable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.Insert(context.Background(), ColumnChange{"id", 7}, ColumnChange{"b", []byte("new")}); err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Refresh(context.Background(), map[string]any{"id": 7})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	var b []byte
	if !rows.Next() || rows.Scan(&id, &b) != nil || id != 7 || string(b) != "new" {
		t.Fatal(id, b, rows.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorInsertRejectsNullKey(t *testing.T) {
	id := &cursorIdentity{table: "`s`.`t`", keys: []string{"id"}, columns: []string{"id", "b"}, locking: true}
	c := &conn{session: &handshakeSession{mode: ModeMySQL}, activeTx: &tx{}}
	handle := &resourceHandle{session: new(sql.Tx)}
	c.resources.records = map[*resourceEpoch]*resourceRecord{nil: {key: handle.key, cursor: &cursorResource{identity: id}}}
	if _, _, err := c.cursorMutationLocked(cursorMutationRequest{cursor: &Cursor{handle: handle}, operation: cursorInsert, changes: []ColumnChange{{"id", []byte(nil)}, {"b", []byte("v")}}}); err == nil {
		t.Fatal("NULL primary key accepted before insert")
	}
}

func TestCursorMutationFailure(t *testing.T) {
	for _, kind := range []string{testCursorRowMissing, testCursorWarning, testCursorPrepareEnded, testCursorInsertEnded, testCursorLockEnded} {
		t.Run(kind, func(t *testing.T) {
			const query = "SELECT id,b FROM s.t"
			db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				serveCursorMutationFailure(ctx, t, p, kind)
			})
			defer func() { _ = db.Close() }()
			tr, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tr.Rollback() }()
			cur, err := OpenCursor(context.Background(), tr, query, CursorOptions{Updatable: true})
			if err != nil {
				t.Fatal(err)
			}
			if kind == testCursorInsertEnded {
				err = cur.Insert(context.Background(), ColumnChange{"id", 7}, ColumnChange{"b", []byte("v")})
			} else {
				err = cur.Update(context.Background(), map[string]any{"id": 7}, ColumnChange{"b", []byte("v")})
			}
			if err == nil {
				t.Fatal("mutation failure reported success")
			}
			if kind == testCursorRowMissing && !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
			if (kind == testCursorPrepareEnded || kind == testCursorInsertEnded || kind == testCursorLockEnded) && !errors.Is(err, ErrResource) {
				t.Fatal(err)
			}
			if _, err = cur.Fetch(context.Background(), CursorPosition{}, 1); !errors.Is(err, ErrResource) {
				t.Fatal("failed mutation left cursor usable", err)
			}
			if err = tr.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorOracleUpdate(t *testing.T) {
	const query = `SELECT "id","b" FROM "s"."t"`
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !servePrepare(ctx, t, p, query, 65, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(65)) || !sendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 253), []byte{254, 0, 0, 69, 0}) || !serveTableRows(ctx, t, p, 5, []string{"FROM ALL_TABLES t", "ALL_TAB_COLUMNS"}, []byte{253, 253, 253, 253, 253, 253, 253, 253}, []any{"s", "t", blobBaseTable, nil, "id", typeNameNumber, "NO", "1"}, []any{"s", "t", blobBaseTable, nil, "b", "VARCHAR2", "NO", nil}) || !servePrepareStatus(ctx, t, p, `UPDATE "s"."t" SET "b" = ? WHERE "id" = ?`, 66, 2, nil, 5) || !serveTableRows(ctx, t, p, 5, []string{`SELECT 1 FROM "s"."t" WHERE "id" =  7  FOR UPDATE`}, []byte{8}, []any{"1"}) {
			return
		}
		execute := append(executeHeader(66), 0, 1, 253, 0, 8, 0, 1, 'v', 7, 0, 0, 0, 0, 0, 0, 0)
		if !readCommand(ctx, t, p, execute) || !sendPackets(ctx, t, p, []byte{0, 1, 0, 5, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 66)) || !readCommand(ctx, t, p, stmtPacket(25, 65)) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tr, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	cur, err := OpenCursor(context.Background(), tr, query, CursorOptions{Updatable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.Update(context.Background(), map[string]any{"id": 7}, ColumnChange{"b", "v"}); err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorRefreshMissing(t *testing.T) {
	const query = "SELECT id,b FROM s.t"
	const refresh = "SELECT `id` AS `id`,`b` AS `b` FROM `s`.`t` WHERE `id` = ?"
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !servePrepare(ctx, t, p, query, 69, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(69)) || !sendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 67, 0}) || !serveTableDictionary(ctx, t, p, 3, tableDictionary()...) || !servePrepareStatus(ctx, t, p, refresh, 70, 1, nil, 3) || !readCommand(ctx, t, p, append(executeHeader(70), 0, 1, 8, 0, 7, 0, 0, 0, 0, 0, 0, 0)) || !sendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 3, 0}, []byte{254, 0, 0, 3, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 70)) || !readCommand(ctx, t, p, stmtPacket(25, 69)) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tr, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	cur, err := OpenCursor(context.Background(), tr, query, CursorOptions{Updatable: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Refresh(context.Background(), map[string]any{"id": 7}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(context.Background(), CursorPosition{}, 1); !errors.Is(err, ErrResource) {
		t.Fatal(err)
	}
	if err = tr.Rollback(); err != nil {
		t.Fatal("missing row discarded the caller transaction", err)
	}
}

const testCursorRowMissing = "missing"

const testCursorWarning = "warning"

const testCursorPrepareEnded = "prepare-ended-transaction"

const testCursorInsertEnded = "insert-ended-transaction"

const testCursorLockEnded = "lock-ended-transaction"

func serveCursorMutationFailure(ctx context.Context, t *testing.T, p *wire.PacketIO, kind string) {
	t.Helper()
	const query = "SELECT id,b FROM s.t"
	if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !servePrepare(ctx, t, p, query, 63, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(63)) || !sendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 67, 0}) || !serveTableDictionary(ctx, t, p, 3, tableDictionary()...) {
		return
	}
	status := byte(3)
	if kind == testCursorPrepareEnded || kind == testCursorInsertEnded {
		status = 2
	}
	mutation := "UPDATE `s`.`t` SET `b` = ? WHERE `id` = ?"
	if kind == testCursorInsertEnded {
		mutation = "INSERT INTO `s`.`t` (`id`,`b`) VALUES (?,?)"
	}
	if !servePrepareStatus(ctx, t, p, mutation, 64, 2, nil, status) {
		return
	}
	if kind != testCursorPrepareEnded && kind != testCursorInsertEnded {
		var rows [][]any
		if kind != testCursorRowMissing {
			rows = [][]any{{"1"}}
		}
		lockStatus := byte(3)
		if kind == testCursorLockEnded {
			lockStatus = 2
		}
		if !serveTableRows(ctx, t, p, lockStatus, []string{testForUpdate}, []byte{8}, rows...) {
			return
		}
		if kind == testCursorWarning {
			execute := append(executeHeader(64), 0, 1, 252, 0, 8, 0, 1, 'v', 7, 0, 0, 0, 0, 0, 0, 0)
			if !readCommand(ctx, t, p, execute) || !sendPackets(ctx, t, p, []byte{0, 1, 0, 3, 0, 1, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 64)) {
				return
			}
		}
	}
	if !readCommand(ctx, t, p, stmtPacket(25, 63)) {
		return
	}
	if kind != testCursorWarning && !readCommand(ctx, t, p, stmtPacket(25, 64)) {
		return
	}
	if !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
		return
	}
	p.ResetSequence()
	serveQuit(t, p)
}
