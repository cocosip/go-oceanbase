package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testCacheSQL = "INSERT INTO u VALUES (?)"

func optimizationDB(t *testing.T, mode Mode, options ExecutionOptions, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	connector, err := NewConnector(Config{User: testUser, Mode: mode, Execution: options, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, status) || !serveBaseline(ctx, t, p, status) {
			return
		}
		serve(context.Background(), p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	return db
}

func TestStatementCacheLRU(t *testing.T) {
	queries := []string{testBatchSQL, testCacheSQL, testBatchSQL, "INSERT INTO v VALUES (?)", testCacheSQL}
	ids := []uint32{7, 8, 7, 9, 10}
	db := optimizationDB(t, ModeMySQL, ExecutionOptions{StatementCacheSize: 2}, func(ctx context.Context, p *wire.PacketIO) {
		for i, query := range queries {
			if i != 2 && !servePrepare(ctx, t, p, query, ids[i], 1, nil) {
				return
			}
			if !serveOK(ctx, t, p, optimizationExecute(ids[i], int64(i), false), 2) {
				return
			}
			if i == 3 && !readCommand(ctx, t, p, stmtPacket(25, 8)) || i == 4 && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
				return
			}
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 9)) || !readCommand(ctx, t, p, stmtPacket(25, 10)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	for i, query := range queries {
		if _, err := db.ExecContext(context.Background(), query, i); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStatementCacheInvalidation(t *testing.T) {
	for _, sqlLimit := range []int{1, 1024} {
		t.Run(fmt.Sprint(sqlLimit), func(t *testing.T) {
			db := optimizationDB(t, ModeMySQL, ExecutionOptions{StatementCacheSize: 1, StatementCacheSQLLimit: sqlLimit}, func(ctx context.Context, p *wire.PacketIO) {
				for i := uint32(7); i <= 8; i++ {
					if !servePrepare(ctx, t, p, testBatchSQL, i, 1, nil) || !serveOK(ctx, t, p, optimizationExecute(i, 1, false), 2) || !readCommand(ctx, t, p, stmtPacket(25, i)) {
						return
					}
					if i == 7 && !serveOK(ctx, t, p, []byte("\x03SET sql_mode='ANSI_QUOTES'"), 2) {
						return
					}
				}
				p.ResetSequence()
				serveQuit(t, p)
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

func TestStatementCacheFullReset(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, 6) || !serveOK(ctx, t, p, optimizationExecute(7, 1, false), 6) {
			return
		}
		p.ResetSequence()
		if data, err := p.ReadMessage(ctx); err == nil {
			t.Errorf("Full reset reused old session: %x", data)
		}
	})
	c.cfg.Execution = ExecutionOptions{StatementCacheSize: 1, StatementCacheSQLLimit: 1024}
	c.cfg.ResetMode = ResetFull
	if _, err := c.ExecContext(context.Background(), testBatchSQL, []driver.NamedValue{{Ordinal: 1, Value: int64(1)}}); err != nil {
		t.Fatal(err)
	}
	if c.statements.order.Len() != 1 {
		t.Fatal("statement was not cached")
	}
	if err := c.ResetSession(context.Background()); !errors.Is(err, driver.ErrBadConn) || c.IsValid() {
		t.Fatal("Full reset retained physical statement IDs", err)
	}
}

func optimizationExecute(id uint32, value int64, reuse bool) []byte {
	b := append(executeHeader(id), 0, 1)
	if reuse {
		b[len(b)-1] = 0
	} else {
		b = append(b, 8, 0)
	}
	return binary.LittleEndian.AppendUint64(b, uint64(value))
}

func TestStatementCache(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			queries := []string{testBatchSQL, testBatchSQL, testCacheSQL, testBatchSQL}
			ids := []uint32{7, 7, 8, 9}
			status := byte(2)
			if mode == ModeOracle {
				status = 6
			}
			db := optimizationDB(t, mode, ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}, func(ctx context.Context, p *wire.PacketIO) {
				for i, query := range queries {
					if i != 1 && !servePrepareStatus(ctx, t, p, query, ids[i], 1, nil, status) {
						return
					}
					if !readCommand(ctx, t, p, optimizationExecute(ids[i], int64(i+1), i == 1)) || !sendPackets(ctx, t, p, commandPacket(1, 0, status)) {
						return
					}
					if i >= 2 && !readCommand(ctx, t, p, stmtPacket(25, ids[i-1])) {
						return
					}
				}
				if !readCommand(ctx, t, p, stmtPacket(25, 9)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
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

func TestStatementCacheFailureEvicts(t *testing.T) {
	db := optimizationDB(t, ModeMySQL, ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}, func(ctx context.Context, p *wire.PacketIO) {
		for i := int64(1); i <= 2; i++ {
			if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !readCommand(ctx, t, p, optimizationExecute(7, i, false)) {
				return
			}
			response := commandPacket(1, 0, 2)
			if i == 1 {
				response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'x'}
			}
			if !sendPackets(ctx, t, p, response) {
				return
			}
			if i == 1 && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
				return
			}
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), testBatchSQL, 1); err == nil {
		t.Fatal("SQL failure lost")
	}
	if _, err := db.ExecContext(context.Background(), testBatchSQL, 2); err != nil {
		t.Fatal(err)
	}
}
