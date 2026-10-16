package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testAutocommitOn    = "SET autocommit=1"
	testMySQLReadWrite  = "START TRANSACTION READ WRITE"
	testSerializableSQL = "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
	testAutocommitOff   = "SET autocommit=0"
	testCommitSQL       = "COMMIT"
	testRollbackSQL     = "ROLLBACK"
	testOracleReadWrite = "SET TRANSACTION READ WRITE"
)

type txStep struct {
	query  string
	status byte
}

func serveTxSteps(ctx context.Context, t *testing.T, p *wire.PacketIO, steps []txStep) bool {
	for _, step := range steps {
		if !serveOK(ctx, t, p, append([]byte{3}, step.query...), step.status) {
			return false
		}
	}
	return true
}
func txDB(t *testing.T, mode Mode, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	connector, err := NewConnector(Config{User: testUser, Mode: mode, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, status) || !serveBaseline(ctx, t, p, status) {
			return
		}
		serve(context.Background(), p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(connector)
}
func TestTxOptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     Mode
		opts     sql.TxOptions
		steps    []txStep
		end      []txStep
		rollback bool
	}{
		{"mysql-default", ModeMySQL, sql.TxOptions{}, []txStep{{testMySQLReadWrite, 3}}, []txStep{{testCommitSQL, 2}}, false},
		{"mysql-readonly", ModeMySQL, sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}, []txStep{{testSerializableSQL, 2}, {"START TRANSACTION READ ONLY", 3}}, []txStep{{testRollbackSQL, 2}}, true},
		{"oracle-default", ModeOracle, sql.TxOptions{}, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}, []txStep{{testCommitSQL, 4}, {testAutocommitOn, 6}}, false},
		{"oracle-readonly", ModeOracle, sql.TxOptions{ReadOnly: true}, []txStep{{testAutocommitOff, 4}, {"SET TRANSACTION READ ONLY", 5}}, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}, true},
		{"oracle-serializable", ModeOracle, sql.TxOptions{Isolation: sql.LevelSerializable}, []txStep{{testAutocommitOff, 4}, {testSerializableSQL, 5}}, []txStep{{testCommitSQL, 4}, {testAutocommitOn, 6}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := txDB(t, tc.mode, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, tc.steps) {
					return
				}
				active := byte(3)
				ready := byte(2)
				if tc.mode == ModeOracle {
					active = 5
					ready = 6
				}
				if !serveTxSteps(ctx, t, p, []txStep{{"UPDATE t SET v=1", active}}) || !serveTxSteps(ctx, t, p, tc.end) {
					return
				}
				if !serveOK(ctx, t, p, []byte{14}, ready) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			tx, err := db.BeginTx(context.Background(), &tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(context.Background(), "UPDATE t SET v=1"); err != nil {
				t.Fatal(err)
			}
			if tc.rollback {
				err = tx.Rollback()
			} else {
				err = tx.Commit()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); !errors.Is(err, sql.ErrTxDone) {
				t.Fatal("finished tx reused", err)
			}
			if err = db.PingContext(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestTxUnsupportedOptions(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: "mysql", ModeOracle: "oracle"}[mode], func(t *testing.T) {
			db := txDB(t, mode, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); serveQuit(t, p) })
			defer func() { _ = db.Close() }()
			pinned, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			opts := []sql.TxOptions{{Isolation: sql.LevelReadUncommitted}, {Isolation: sql.LevelSnapshot}, {Isolation: sql.IsolationLevel(99)}}
			if mode == ModeOracle {
				opts = append(opts, sql.TxOptions{Isolation: sql.LevelRepeatableRead}, sql.TxOptions{Isolation: sql.LevelReadUncommitted}, sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
			}
			for _, opt := range opts {
				if _, err = pinned.BeginTx(context.Background(), &opt); !errors.Is(err, ErrUnsupported) {
					t.Fatal(opt, err)
				}
			}
			if err = pinned.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestCommitUnknown(t *testing.T) {
	var commits atomic.Int32
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) {
			return
		}
		if readQuery(ctx, t, p, testCommitSQL) {
			commits.Add(1)
		}
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = tx.Commit()
	if !errors.Is(err, ErrCommitUnknown) || errors.Is(err, driver.ErrBadConn) || commits.Load() != 1 || db.Stats().Idle != 0 {
		t.Fatal(commits.Load(), err)
	}
	if err = tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("rollback claimed undo after unknown commit", err)
	}
}
func TestTxCleanupFailure(t *testing.T) {
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}, {testCommitSQL, 4}}) {
			return
		}
		if !readQuery(ctx, t, p, testAutocommitOn) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("dirty session reused")
		}
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = tx.Commit()
	if !errors.Is(err, ErrTxCleanup) || errors.Is(err, ErrCommitUnknown) || db.Stats().Idle != 0 {
		t.Fatal("known commit confused with restoration", err)
	}
}

func TestSavepoints(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: "mysql", ModeOracle: "oracle"}[mode], func(t *testing.T) {
			start := []txStep{{testMySQLReadWrite, 3}}
			end := []txStep{{testRollbackSQL, 2}}
			active := byte(3)
			quoted := "`a\"``;x`"
			if mode == ModeOracle {
				start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
				end = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}
				active = 5
				quoted = "\"a\"\"`;x\""
			}
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, start) {
					return
				}
				steps := []txStep{{"SAVEPOINT " + quoted, active}, {"ROLLBACK TO SAVEPOINT " + quoted, active}}
				if mode == ModeMySQL {
					steps = append(steps, txStep{"RELEASE SAVEPOINT " + quoted, active})
				}
				if !serveTxSteps(ctx, t, p, steps) || !serveTxSteps(ctx, t, p, end) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"", "bad\x00name"} {
				if err = Savepoint(context.Background(), tx, name); err == nil {
					t.Fatal("invalid savepoint accepted")
				}
			}
			name := "a\"`;x"
			if err = Savepoint(context.Background(), tx, name); err != nil {
				t.Fatal(err)
			}
			if err = RollbackTo(context.Background(), tx, name); err != nil {
				t.Fatal(err)
			}
			err = ReleaseSavepoint(context.Background(), tx, name)
			if mode == ModeOracle {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := Savepoint(context.Background(), nil, "p"); err == nil {
		t.Fatal("nil tx accepted")
	}
}

func TestTxCancellationRollback(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}, {testRollbackSQL, 2}}) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	// Ping waits until database/sql's automatic rollback returns the physical connection.
	db.SetMaxOpenConns(1)
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestTxSetupFailure(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testSerializableSQL, 2}}) {
			return
		}
		if !readQuery(ctx, t, p, testMySQLReadWrite) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("partially configured connection retained")
		}
	})
	defer func() { _ = db.Close() }()
	_, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if !errors.As(err, new(*Error)) || errors.Is(err, driver.ErrBadConn) || db.Stats().Idle != 0 {
		t.Fatal(err)
	}
}
