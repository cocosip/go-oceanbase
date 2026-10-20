package cursor_test

import (
	context "context"
	testing "testing"
	_ "time/tzdata"

	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func cursorExecuteFixture(id uint32) []byte {
	b := publictest.ExecuteHeader(id)
	b[5] = 1
	return b
}

func cursorBaseColumn(name string, typ byte) []byte {
	b := []byte{}
	for _, v := range []string{"def", "s", "t", "t", name, name} {
		b = append(b, byte(len(v)))
		b = append(b, v...)
	}
	return append(b, 12, 63, 0, 64, 0, 0, 0, typ, 0, 0, 0, 0, 0)
}

const testCursorInsertEnded = "insert-ended-transaction"

const testCursorRowMissing = "missing"

func serveCursorMutationFailure(ctx context.Context, t *testing.T, p *wire.PacketIO, kind string) {
	t.Helper()
	const query = "SELECT id,b FROM s.t"
	if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !publictest.ServePrepare(ctx, t, p, query, 63, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(63)) || !testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", 252), []byte{254, 0, 0, 67, 0}) || !publictest.ServeTableDictionary(ctx, t, p, 3, publictest.TableDictionary()...) {
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
	if !publictest.ServePrepareStatus(ctx, t, p, mutation, 64, 2, nil, status) {
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
		if !publictest.ServeTableRows(ctx, t, p, lockStatus, []string{publictest.TestForUpdate}, []byte{8}, rows...) {
			return
		}
		if kind == testCursorWarning {
			execute := append(publictest.ExecuteHeader(64), 0, 1, 252, 0, 8, 0, 1, 'v', 7, 0, 0, 0, 0, 0, 0, 0)
			if !testpeer.ReadCommand(ctx, t, p, execute) || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 3, 0, 1, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 64)) {
				return
			}
		}
	}
	if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 63)) {
		return
	}
	if kind != testCursorWarning && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 64)) {
		return
	}
	if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

const testCursorLockEnded = "lock-ended-transaction"

const testCursorPrepareEnded = "prepare-ended-transaction"

const testCursorWarning = "warning"

func serveUpdatableEpochCursor(ctx context.Context, t *testing.T, p *wire.PacketIO, mode config.Mode, typ byte) bool {
	t.Helper()
	const columnsDictionary = "ALL_TAB_COLS"
	status := byte(3)
	steps := []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}
	if mode == config.ModeOracle {
		status = 5
		steps = []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}
	}
	if !publictest.ServeTxSteps(ctx, t, p, steps) || !publictest.ServePrepareStatus(ctx, t, p, cursorEpochQuery, 81, 0, nil, status) ||
		!testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(81)) ||
		!testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", typ), []byte{254, 0, 0, status | 64, 0}) {
		return false
	}
	if mode == config.ModeMySQL {
		return publictest.ServeTableDictionary(ctx, t, p, status, publictest.TableDictionary()...)
	}
	typeName := "VARCHAR2"
	if typ == 210 {
		typeName = "BLOB"
	}
	return publictest.ServeTableRows(ctx, t, p, status, []string{"FROM ALL_TABLES t", columnsDictionary}, []byte{253, 253, 253, 253, 253, 253, 253, 253},
		[]any{"s", "t", publictest.BaseTable, nil, "id", valueformat.TypeNameNumber, "NO", "1"},
		[]any{"s", "t", publictest.BaseTable, nil, "b", typeName, "NO", nil})
}

const cursorEpochQuery = "SELECT id,b FROM s.t"
