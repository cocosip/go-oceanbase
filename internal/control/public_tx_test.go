package control_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	atomic "sync/atomic"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTxSetupFailure(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testSerializableSQL, 2}}) {
			return
		}
		if !testpeer.ReadQuery(ctx, t, p, testMySQLReadWrite) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("partially configured connection retained")
		}
	})
	defer func() { _ = db.Close() }()
	_, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if !errors.As(err, new(*oceanbase.Error)) || errors.Is(err, driver.ErrBadConn) || db.Stats().Idle != 0 {
		t.Fatal(err)
	}
}

func TestTxCancellationRollback(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}, {testRollbackSQL, 2}}) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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

func TestSavepoints(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(map[config.Mode]string{config.ModeMySQL: "mysql", config.ModeOracle: "oracle"}[mode], func(t *testing.T) {
			start := []txStep{{testMySQLReadWrite, 3}}
			end := []txStep{{testRollbackSQL, 2}}
			active := byte(3)
			quoted := "`a\"``;x`"
			if mode == config.ModeOracle {
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
				if mode == config.ModeMySQL {
					steps = append(steps, txStep{"RELEASE SAVEPOINT " + quoted, active})
				}
				if !serveTxSteps(ctx, t, p, steps) || !serveTxSteps(ctx, t, p, end) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"", "bad\x00name"} {
				if err = oceanbase.Savepoint(context.Background(), tx, name); err == nil {
					t.Fatal("invalid savepoint accepted")
				}
			}
			name := "a\"`;x"
			if err = oceanbase.Savepoint(context.Background(), tx, name); err != nil {
				t.Fatal(err)
			}
			if err = oceanbase.RollbackTo(context.Background(), tx, name); err != nil {
				t.Fatal(err)
			}
			err = oceanbase.ReleaseSavepoint(context.Background(), tx, name)
			if mode == config.ModeOracle {
				if !errors.Is(err, dberrors.ErrUnsupported) {
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
	if err := oceanbase.Savepoint(context.Background(), nil, "p"); err == nil {
		t.Fatal("nil tx accepted")
	}
}

func TestTxCleanupFailure(t *testing.T) {
	db := txDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}, {testCommitSQL, 4}}) {
			return
		}
		if !testpeer.ReadQuery(ctx, t, p, testAutocommitOn) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
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
	if !errors.Is(err, oceanbase.ErrTxCleanup) || errors.Is(err, oceanbase.ErrCommitUnknown) || db.Stats().Idle != 0 {
		t.Fatal("known commit confused with restoration", err)
	}
}

func TestCommitUnknown(t *testing.T) {
	var commits atomic.Int32
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) {
			return
		}
		if testpeer.ReadQuery(ctx, t, p, testCommitSQL) {
			commits.Add(1)
		}
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = tx.Commit()
	if !errors.Is(err, oceanbase.ErrCommitUnknown) || errors.Is(err, driver.ErrBadConn) || commits.Load() != 1 || db.Stats().Idle != 0 {
		t.Fatal(commits.Load(), err)
	}
	if err = tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("rollback claimed undo after unknown commit", err)
	}
}

func TestTxUnsupportedOptions(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(map[config.Mode]string{config.ModeMySQL: "mysql", config.ModeOracle: "oracle"}[mode], func(t *testing.T) {
			db := txDB(t, mode, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); testpeer.ServeQuit(t, p) })
			defer func() { _ = db.Close() }()
			pinned, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			opts := []sql.TxOptions{{Isolation: sql.LevelReadUncommitted}, {Isolation: sql.LevelSnapshot}, {Isolation: sql.IsolationLevel(99)}}
			if mode == config.ModeOracle {
				opts = append(opts, sql.TxOptions{Isolation: sql.LevelRepeatableRead}, sql.TxOptions{Isolation: sql.LevelReadUncommitted}, sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
			}
			for _, opt := range opts {
				if _, err = pinned.BeginTx(context.Background(), &opt); !errors.Is(err, dberrors.ErrUnsupported) {
					t.Fatal(opt, err)
				}
			}
			if err = pinned.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTxOptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     config.Mode
		opts     sql.TxOptions
		steps    []txStep
		end      []txStep
		rollback bool
	}{
		{"mysql-default", config.ModeMySQL, sql.TxOptions{}, []txStep{{testMySQLReadWrite, 3}}, []txStep{{testCommitSQL, 2}}, false},
		{"mysql-readonly", config.ModeMySQL, sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}, []txStep{{testSerializableSQL, 2}, {"START TRANSACTION READ ONLY", 3}}, []txStep{{testRollbackSQL, 2}}, true},
		{"oracle-default", config.ModeOracle, sql.TxOptions{}, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}, []txStep{{testCommitSQL, 4}, {testAutocommitOn, 6}}, false},
		{"oracle-readonly", config.ModeOracle, sql.TxOptions{ReadOnly: true}, []txStep{{testAutocommitOff, 4}, {"SET TRANSACTION READ ONLY", 5}}, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}, true},
		{"oracle-serializable", config.ModeOracle, sql.TxOptions{Isolation: sql.LevelSerializable}, []txStep{{testAutocommitOff, 4}, {testSerializableSQL, 5}}, []txStep{{testCommitSQL, 4}, {testAutocommitOn, 6}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := txDB(t, tc.mode, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, tc.steps) {
					return
				}
				active := byte(3)
				ready := byte(2)
				if tc.mode == config.ModeOracle {
					active = 5
					ready = 6
				}
				if !serveTxSteps(ctx, t, p, []txStep{{"UPDATE t SET v=1", active}}) || !serveTxSteps(ctx, t, p, tc.end) {
					return
				}
				if !testpeer.ServeOK(ctx, t, p, []byte{14}, ready) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
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
