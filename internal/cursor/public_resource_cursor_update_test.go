package cursor_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCursorRefreshMissing(t *testing.T) {
	const query = "SELECT id,b FROM s.t"
	const refresh = "SELECT `id` AS `id`,`b` AS `b` FROM `s`.`t` WHERE `id` = ?"
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !publictest.ServePrepare(ctx, t, p, query, 69, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(69)) || !testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 67, 0}) || !publictest.ServeTableDictionary(ctx, t, p, 3, publictest.TableDictionary()...) || !publictest.ServePrepareStatus(ctx, t, p, refresh, 70, 1, nil, 3) || !testpeer.ReadCommand(ctx, t, p, append(publictest.ExecuteHeader(70), 0, 1, 8, 0, 7, 0, 0, 0, 0, 0, 0, 0)) || !testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 3, 0}, []byte{254, 0, 0, 3, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 70)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 69)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tr, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	cur, err := oceanbase.OpenCursor(context.Background(), tr, query, oceanbase.CursorOptions{Updatable: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Refresh(context.Background(), map[string]any{"id": 7}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(context.Background(), oceanbase.CursorPosition{}, 1); !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal(err)
	}
	if err = tr.Rollback(); err != nil {
		t.Fatal("missing row discarded the caller transaction", err)
	}
}

func TestCursorOracleUpdate(t *testing.T) {
	const query = `SELECT "id","b" FROM "s"."t"`
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !publictest.ServePrepare(ctx, t, p, query, 65, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(65)) || !testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 253), []byte{254, 0, 0, 69, 0}) || !publictest.ServeTableRows(ctx, t, p, 5, []string{"FROM ALL_TABLES t", "ALL_TAB_COLS"}, []byte{253, 253, 253, 253, 253, 253, 253, 253}, []any{"s", "t", publictest.BaseTable, nil, "id", valueformat.TypeNameNumber, "NO", "1"}, []any{"s", "t", publictest.BaseTable, nil, "b", "VARCHAR2", "NO", nil}) || !publictest.ServePrepareStatus(ctx, t, p, `UPDATE "s"."t" SET "b" = ? WHERE "id" = ?`, 66, 2, nil, 5) || !publictest.ServeTableRows(ctx, t, p, 5, []string{`SELECT 1 FROM "s"."t" WHERE "id" =  7  FOR UPDATE`}, []byte{8}, []any{"1"}) {
			return
		}
		execute := append(publictest.ExecuteHeader(66), 0, 1, 253, 0, 8, 0, 1, 'v', 7, 0, 0, 0, 0, 0, 0, 0)
		if !testpeer.ReadCommand(ctx, t, p, execute) || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 5, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 66)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 65)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tr, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	cur, err := oceanbase.OpenCursor(context.Background(), tr, query, oceanbase.CursorOptions{Updatable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.Update(context.Background(), map[string]any{"id": 7}, oceanbase.ColumnChange{Name: "b", Value: "v"}); err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorMutationFailure(t *testing.T) {
	for _, kind := range []string{testCursorRowMissing, testCursorWarning, testCursorPrepareEnded, testCursorInsertEnded, testCursorLockEnded} {
		t.Run(kind, func(t *testing.T) {
			const query = "SELECT id,b FROM s.t"
			db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				serveCursorMutationFailure(ctx, t, p, kind)
			})
			defer func() { _ = db.Close() }()
			tr, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tr.Rollback() }()
			cur, err := oceanbase.OpenCursor(context.Background(), tr, query, oceanbase.CursorOptions{Updatable: true})
			if err != nil {
				t.Fatal(err)
			}
			if kind == testCursorInsertEnded {
				err = cur.Insert(context.Background(), oceanbase.ColumnChange{Name: "id", Value: 7}, oceanbase.ColumnChange{Name: "b", Value: []byte("v")})
			} else {
				err = cur.Update(context.Background(), map[string]any{"id": 7}, oceanbase.ColumnChange{Name: "b", Value: []byte("v")})
			}
			if err == nil {
				t.Fatal("mutation failure reported success")
			}
			if kind == testCursorRowMissing && !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
			if (kind == testCursorPrepareEnded || kind == testCursorInsertEnded || kind == testCursorLockEnded) && !errors.Is(err, oceanbase.ErrResource) {
				t.Fatal(err)
			}
			if _, err = cur.Fetch(context.Background(), oceanbase.CursorPosition{}, 1); !errors.Is(err, oceanbase.ErrResource) {
				t.Fatal("failed mutation left cursor usable", err)
			}
			if err = tr.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorInsertRefresh(t *testing.T) {
	const query = "SELECT id,b FROM s.t"
	const refresh = "SELECT `id` AS `id`,`b` AS `b` FROM `s`.`t` WHERE `id` = ?"
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !publictest.ServePrepare(ctx, t, p, query, 57, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(57)) || !testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 67, 0}) || !publictest.ServeTableDictionary(ctx, t, p, 3, publictest.TableDictionary()...) {
			return
		}
		if !publictest.ServePrepareStatus(ctx, t, p, "INSERT INTO `s`.`t` (`id`,`b`) VALUES (?,?)", 62, 2, nil, 3) {
			return
		}
		insert := append(publictest.ExecuteHeader(62), 0, 1, 8, 0, 252, 0, 7, 0, 0, 0, 0, 0, 0, 0, 3, 'n', 'e', 'w')
		if !testpeer.ReadCommand(ctx, t, p, insert) || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 3, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 62)) || !publictest.ServePrepareStatus(ctx, t, p, refresh, 58, 1, nil, 3) {
			return
		}
		execute := append(publictest.ExecuteHeader(58), 0, 1, 8, 0, 7, 0, 0, 0, 0, 0, 0, 0)
		if !testpeer.ReadCommand(ctx, t, p, execute) || !testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 3, 0}, append(binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), 3, 'n', 'e', 'w'), []byte{254, 0, 0, 3, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 58)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 57)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tr, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	cur, err := oceanbase.OpenCursor(context.Background(), tr, query, oceanbase.CursorOptions{Updatable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.Insert(context.Background(), oceanbase.ColumnChange{Name: "id", Value: 7}, oceanbase.ColumnChange{Name: "b", Value: []byte("new")}); err != nil {
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

func TestCursorUpdateIdentity(t *testing.T) {
	const query = "SELECT id,b FROM s.t"
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !publictest.ServePrepare(ctx, t, p, query, 56, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(56)) || !testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 67, 0}) || !publictest.ServeTableDictionary(ctx, t, p, 3, publictest.TableDictionary()...) {
			return
		}
		for _, op := range []string{"UPDATE", "DELETE"} {
			query := "UPDATE `s`.`t` SET `b` = ? WHERE `id` = ?"
			count, affected := 2, byte(0)
			execute := append(publictest.ExecuteHeader(61), 0, 1, 252, 0, 8, 0, 9)
			execute = append(execute, []byte("unchanged")...)
			execute = binary.LittleEndian.AppendUint64(execute, 7)
			if op == "DELETE" {
				query = "DELETE FROM `s`.`t` WHERE `id` = ?"
				count, affected = 1, 1
				execute = append(publictest.ExecuteHeader(61), 0, 1, 8, 0, 7, 0, 0, 0, 0, 0, 0, 0)
			}
			if !publictest.ServePrepareStatus(ctx, t, p, query, 61, count, nil, 3) || !publictest.ServeTableRows(ctx, t, p, 3, []string{"SELECT 1 FROM `s`.`t` WHERE `id` =  7  FOR UPDATE"}, []byte{8}, []any{"1"}) || !testpeer.ReadCommand(ctx, t, p, execute) || !testpeer.SendPackets(ctx, t, p, []byte{0, affected, 0, 3, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 61)) {
				return
			}
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 56)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tr, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	cur, err := oceanbase.OpenCursor(context.Background(), tr, query, oceanbase.CursorOptions{Updatable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.Update(context.Background(), map[string]any{"id": 7}, oceanbase.ColumnChange{Name: "b", Value: []byte("unchanged")}); err != nil {
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
