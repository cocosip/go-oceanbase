package statement_test

import (
	context "context"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestStatementCacheFailureEvicts(t *testing.T) {
	db := optimizationDB(t, oceanbase.ModeMySQL, oceanbase.ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}, func(ctx context.Context, p *wire.PacketIO) {
		for i := int64(1); i <= 2; i++ {
			if !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !testpeer.ReadCommand(ctx, t, p, optimizationExecute(7, i, false)) {
				return
			}
			response := publictest.CommandPacket(1, 0, 2)
			if i == 1 {
				response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'x'}
			}
			if !testpeer.SendPackets(ctx, t, p, response) {
				return
			}
			if i == 1 && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
				return
			}
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), testBatchSQL, 1); err == nil {
		t.Fatal("SQL failure lost")
	}
	if _, err := db.ExecContext(context.Background(), testBatchSQL, 2); err != nil {
		t.Fatal(err)
	}
}

func TestStatementCache(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			queries := []string{testBatchSQL, testBatchSQL, testCacheSQL, testBatchSQL}
			ids := []uint32{7, 7, 8, 9}
			status := byte(2)
			if mode == oceanbase.ModeOracle {
				status = 6
			}
			db := optimizationDB(t, mode, oceanbase.ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}, func(ctx context.Context, p *wire.PacketIO) {
				for i, query := range queries {
					if i != 1 && !publictest.ServePrepareStatus(ctx, t, p, query, ids[i], 1, nil, status) {
						return
					}
					if !testpeer.ReadCommand(ctx, t, p, optimizationExecute(ids[i], int64(i+1), i == 1)) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, status)) {
						return
					}
					if i >= 2 && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, ids[i-1])) {
						return
					}
				}
				if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 9)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			for i, query := range queries {
				res, err := db.ExecContext(context.Background(), query, i+1)
				if err != nil {
					t.Fatal(err)
				}
				if n, err := res.RowsAffected(); err != nil || n != 1 {
					t.Fatal(n, err)
				}
			}
		})
	}
}

func TestStatementCacheFullReset(t *testing.T) {
	c := publictest.OutputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, 6) || !testpeer.ServeOK(ctx, t, p, optimizationExecute(7, 1, false), 6) {
			return
		}
		p.ResetSequence()
		if data, err := p.ReadMessage(ctx); err == nil {
			t.Errorf("Full reset reused old session: %x", data)
		}
	}, func(cfg *config.Config) {
		cfg.Execution = config.ExecutionOptions{StatementCacheSize: 1, StatementCacheSQLLimit: 1024}
		cfg.ResetMode = config.ResetFull
	})
	if _, err := c.ExecContext(context.Background(), testBatchSQL, []driver.NamedValue{{Ordinal: 1, Value: int64(1)}}); err != nil {
		t.Fatal(err)
	}
	if err := c.ResetSession(context.Background()); !errors.Is(err, driver.ErrBadConn) || c.IsValid() {
		t.Fatal("Full reset retained physical statement IDs", err)
	}
}

func TestStatementCacheSurvivesTextQuery(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			status := byte(2)
			if mode == oceanbase.ModeOracle {
				status = 6
			}
			db := optimizationDB(t, mode, oceanbase.ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) || !testpeer.ServeOK(ctx, t, p, optimizationExecute(7, 1, false), status) {
					return
				}
				eof := []byte{254, 0, 0, status, 0}
				if !testpeer.ReadCommand(ctx, t, p, []byte("\x03SELECT 1")) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), eof, []byte{1, '1'}, eof) {
					return
				}
				if !testpeer.ServeOK(ctx, t, p, optimizationExecute(7, 2, true), status) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			if _, err := db.ExecContext(context.Background(), testBatchSQL, 1); err != nil {
				t.Fatal(err)
			}
			var value int
			if err := db.QueryRowContext(context.Background(), "SELECT 1").Scan(&value); err != nil || value != 1 {
				t.Fatal(value, err)
			}
			if _, err := db.ExecContext(context.Background(), testBatchSQL, 2); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStatementCacheInvalidation(t *testing.T) {
	for _, sqlLimit := range []int{1, 1024} {
		t.Run(fmt.Sprint(sqlLimit), func(t *testing.T) {
			db := optimizationDB(t, oceanbase.ModeMySQL, oceanbase.ExecutionOptions{StatementCacheSize: 1, StatementCacheSQLLimit: sqlLimit}, func(ctx context.Context, p *wire.PacketIO) {
				for i := uint32(7); i <= 8; i++ {
					if !publictest.ServePrepare(ctx, t, p, testBatchSQL, i, 1, nil) || !testpeer.ServeOK(ctx, t, p, optimizationExecute(i, 1, false), 2) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, i)) {
						return
					}
					if i == 7 && !testpeer.ServeOK(ctx, t, p, []byte("\x03SET sql_mode='ANSI_QUOTES'"), 2) {
						return
					}
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			if _, err := db.ExecContext(context.Background(), testBatchSQL, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(context.Background(), "SET sql_mode='ANSI_QUOTES'"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(context.Background(), testBatchSQL, 1); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStatementCacheLRU(t *testing.T) {
	queries := []string{testBatchSQL, testCacheSQL, testBatchSQL, "INSERT INTO v VALUES (?)", testCacheSQL}
	ids := []uint32{7, 8, 7, 9, 10}
	db := optimizationDB(t, oceanbase.ModeMySQL, oceanbase.ExecutionOptions{StatementCacheSize: 2}, func(ctx context.Context, p *wire.PacketIO) {
		for i, query := range queries {
			if i != 2 && !publictest.ServePrepare(ctx, t, p, query, ids[i], 1, nil) {
				return
			}
			if !testpeer.ServeOK(ctx, t, p, optimizationExecute(ids[i], int64(i), false), 2) {
				return
			}
			if i == 3 && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 8)) || i == 4 && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
				return
			}
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 9)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 10)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	for i, query := range queries {
		if _, err := db.ExecContext(context.Background(), query, i); err != nil {
			t.Fatal(err)
		}
	}
}
