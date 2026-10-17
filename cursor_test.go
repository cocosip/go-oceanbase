package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func cursorExecuteFixture(id uint32) []byte {
	b := executeHeader(id)
	b[5] = 1
	return b
}

func cursorFetchFixture(id, count uint32) []byte {
	return binary.LittleEndian.AppendUint32(stmtPacket(28, id), count)
}

func TestCursorFetch(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 51, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(51)) ||
			!sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 66, 0}) {
			return
		}
		if !readCommand(ctx, t, p, cursorFetchFixture(51, 2)) || !sendPackets(ctx, t, p,
			binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), binary.LittleEndian.AppendUint64([]byte{0, 0}, 8), []byte{254, 0, 0, 130, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 51)) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	cur, err := OpenCursor(ctx, session, query, CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(ctx, CursorPosition{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(ctx, CursorPosition{}, 1); !errors.Is(err, ErrCursorBusy) {
		t.Fatal("unclosed batch allowed a second fetch", err)
	}
	if err = cur.Close(); !errors.Is(err, ErrCursorBusy) {
		t.Fatal("unclosed batch allowed cursor close", err)
	}
	for _, want := range []int64{7, 8} {
		var got int64
		if !rows.Next() || rows.Scan(&got) != nil || got != want {
			t.Fatal(got, want, rows.Err())
		}
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err = cur.Fetch(ctx, CursorPosition{}, 1)
	if err != nil || rows.Next() || rows.Err() != nil {
		t.Fatal("exhausted cursor did not return an empty batch", err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
	if err = session.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCursorPreSendValidation(t *testing.T) {
	for _, opts := range []CursorOptions{{Mode: 255}, {MaxCacheBytes: -1}} {
		if _, err := OpenCursor(context.Background(), new(sql.Conn), "SELECT 1", opts); !errors.Is(err, ErrCursor) {
			t.Fatal(err)
		}
	}
	if _, err := OpenCursor(context.Background(), new(sql.DB), "SELECT 1", CursorOptions{}); !errors.Is(err, ErrResource) {
		t.Fatal(err)
	}
}

func TestCursorStatic(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	for _, limit := range []int64{0, 140} {
		t.Run(map[bool]string{false: "scroll", true: "budget"}[limit != 0], func(t *testing.T) {
			db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !servePrepare(ctx, t, p, query, 52, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(52)) ||
					!sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 66, 0}) || !readCommand(ctx, t, p, cursorFetchFixture(52, 128)) {
					return
				}
				for i := uint64(1); i <= 3; i++ {
					if !sendPackets(ctx, t, p, binary.LittleEndian.AppendUint64([]byte{0, 0}, i)) {
						return
					}
				}
				if !sendPackets(ctx, t, p, []byte{254, 0, 0, 130, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 52)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			session, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			cur, err := OpenCursor(ctx, session, query, CursorOptions{Mode: CursorStatic, MaxCacheBytes: limit})
			if limit != 0 {
				if !errors.Is(err, wire.ErrMessageTooLarge) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				pos   CursorPosition
				count int
				want  []int64
			}{{CursorPosition{CursorAbsolute, 2}, 1, []int64{2}}, {CursorPosition{CursorRelative, -1}, 2, []int64{1, 2}}, {CursorPosition{CursorAbsolute, 0}, 1, nil}, {CursorPosition{}, 1, []int64{1}}, {CursorPosition{CursorAbsolute, 99}, 1, nil}} {
				rows, err := cur.Fetch(ctx, tc.pos, tc.count)
				if err != nil {
					t.Fatal(err)
				}
				var got []int64
				for rows.Next() {
					var id int64
					if err = rows.Scan(&id); err != nil {
						t.Fatal(err)
					}
					got = append(got, id)
				}
				if err = rows.Err(); err != nil || !reflect.DeepEqual(got, tc.want) {
					t.Fatal(got, tc.want, err)
				}
			}
			if err = cur.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorOracleFetchLOB(t *testing.T) {
	const query = "SELECT payload FROM cursor_fixture"
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 53, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(53)) ||
			!sendPackets(ctx, t, p, []byte{1}, textColumn("payload", 210, 63, 0), []byte{254, 0, 0, 70, 0}) {
			return
		}
		fetch := append(cursorFetchFixture(53, 1), 2, 0, 0, 0, 0, 0, 1, 0, 0, 0)
		if !readCommand(ctx, t, p, fetch) || !sendPackets(ctx, t, p,
			[]byte{1}, textColumn("payload", 210, 63, 0), []byte{254, 0, 0, 6, 0},
			complexLenBytes([]byte{0, 0}, lobInlineFixture(testLOBABC)), []byte{254, 0, 0, 134, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 53)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	cur, err := OpenCursor(ctx, session, query, CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(ctx, CursorPosition{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	if !rows.Next() || rows.Scan(&got) != nil || string(got) != testLOBABC {
		t.Fatal(got, rows.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorSensitive(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 54, 0, nil) {
			return
		}
		payload, err := wire.EncodePrepareExecute(54, []byte(query), nil, 4096)
		if err != nil {
			t.Error(err)
			return
		}
		payload[5] = 1
		binary.LittleEndian.PutUint32(payload[len(payload)-16:], 8)
		if !readCommand(ctx, t, p, payload) || !sendPackets(ctx, t, p, combinedHeader(54, 1, 0, 0), textColumn("id", 8, 63, 0), []byte{254, 0, 0, 70, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 1), []byte{254, 0, 0, 70, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) {
			return
		}
		for i, value := range []uint64{7, 9} {
			fetch := append(cursorFetchFixture(54, 1), 32, 0, 1, 0, 0, 0, 1, 0, 0, 0)
			if !readCommand(ctx, t, p, fetch) || !sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 70, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, value), []byte{254, 0, 0, 134, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) {
				t.Errorf("sensitive fetch %d", i)
				return
			}
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 54)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := pinnedSession(t, db)
	defer func() { _ = session.Close() }()
	cur, err := OpenCursor(context.Background(), session, query, CursorOptions{Mode: CursorSensitive})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{7, 9} {
		rows, e := cur.Fetch(context.Background(), CursorPosition{CursorAbsolute, 1}, 1)
		if e != nil {
			t.Fatal(e)
		}
		var got int64
		if !rows.Next() || rows.Scan(&got) != nil || got != want {
			t.Fatal("sensitive fetch reused stale rows", got, want, rows.Err())
		}
		if e = rows.Close(); e != nil {
			t.Fatal(e)
		}
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorEmptyOpen(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 55, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(55)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 130, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 55)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := pinnedSession(t, db)
	defer func() { _ = session.Close() }()
	cur, err := OpenCursor(context.Background(), session, query, CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(context.Background(), CursorPosition{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorRejectsUnsafeOpen(t *testing.T) {
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := pinnedSession(t, db)
	defer func() { _ = session.Close() }()
	for _, tc := range []struct {
		query   string
		options CursorOptions
		want    error
	}{
		{"SELECT id FROM t; DELETE FROM t", CursorOptions{}, ErrCursor},
		{"SELECT id FROM t", CursorOptions{Mode: CursorSensitive}, ErrUnsupported},
		{"SELECT id FROM t", CursorOptions{Updatable: true}, ErrUnsupported},
	} {
		if _, err := OpenCursor(context.Background(), session, tc.query, tc.options); !errors.Is(err, tc.want) {
			t.Fatal(tc.query, err)
		}
	}
	if err := session.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCursorFetchFailureInvalidates(t *testing.T) {
	const query = "SELECT id FROM t"
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 59, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(59)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 66, 0}) || !readCommand(ctx, t, p, cursorFetchFixture(59, 1)) || !sendPackets(ctx, t, p, []byte{254, 0, 0, 66, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 59)) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := pinnedSession(t, db)
	defer func() { _ = session.Close() }()
	cur, err := OpenCursor(context.Background(), session, query, CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(context.Background(), CursorPosition{}, 1); !errors.Is(err, ErrCursor) {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(context.Background(), CursorPosition{}, 1); !errors.Is(err, ErrResource) {
		t.Fatal("failed fetch left cursor reusable", err)
	}
	if err = session.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCursorTxLifetime(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !servePrepare(ctx, t, p, query, 60, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(60)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 67, 0}) || !readCommand(ctx, t, p, cursorFetchFixture(60, 1)) || !sendPackets(ctx, t, p, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), []byte{254, 0, 0, 131, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 60)) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
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
	openCtx, cancel := context.WithCancel(context.Background())
	cur, err := OpenCursor(openCtx, tr, query, CursorOptions{})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(context.Background(), CursorPosition{}, 1)
	if err != nil {
		t.Fatal("fetch inherited open context", err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(context.Background(), CursorPosition{}, 1); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, ErrResource) {
		t.Fatal("cursor survived transaction", err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorPositionBounds(t *testing.T) {
	for _, tc := range []struct {
		current int64
		pos     CursorPosition
	}{
		{1, CursorPosition{CursorRelative, math.MinInt64}},
		{math.MaxInt64, CursorPosition{}},
		{1, CursorPosition{CursorRelative, math.MaxInt64}},
	} {
		if _, err := cursorStart(tc.current, tc.pos); !errors.Is(err, ErrCursor) {
			t.Fatal(tc, err)
		}
	}
}

func FuzzCursorPosition(f *testing.F) {
	for _, seed := range []struct{ current, offset int64 }{{0, 0}, {1, -1}, {1, math.MinInt64}, {math.MaxInt64, 1}} {
		f.Add(seed.current, seed.offset)
	}
	f.Fuzz(func(t *testing.T, current, offset int64) {
		if current < 0 {
			return
		}
		got, err := cursorStart(current, CursorPosition{CursorRelative, offset})
		want := new(big.Int).Add(big.NewInt(current), big.NewInt(offset))
		valid := want.Sign() >= 0 && want.IsInt64()
		if valid && (err != nil || got != want.Int64()) || !valid && !errors.Is(err, ErrCursor) {
			t.Fatal(current, offset, got, err)
		}
	})
}

func TestCursorDeprecatedEOF(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 67, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(67)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0)) || !readCommand(ctx, t, p, cursorFetchFixture(67, 1)) || !sendPackets(ctx, t, p, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), []byte{254, 0, 0, 130, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 67)) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := pinnedSession(t, db)
	defer func() { _ = session.Close() }()
	if err := session.Raw(func(raw any) error { raw.(*conn).session.capabilities |= wire.ClientDeprecateEOF; return nil }); err != nil {
		t.Fatal(err)
	}
	cur, err := OpenCursor(context.Background(), session, query, CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(context.Background(), CursorPosition{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var got int64
	if !rows.Next() || rows.Scan(&got) != nil || got != 7 {
		t.Fatal(got, rows.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
	if err = session.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCursorOldOracleFetch(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 68, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(68)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 70, 0}) || !readCommand(ctx, t, p, cursorFetchFixture(68, 1)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), []byte{254, 0, 0, 134, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 68)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := pinnedSession(t, db)
	defer func() { _ = session.Close() }()
	if err := session.Raw(func(raw any) error { raw.(*conn).session.obVersion = "2.2.4"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCursor(context.Background(), session, query, CursorOptions{Mode: CursorSensitive}); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	cur, err := OpenCursor(context.Background(), session, query, CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(context.Background(), CursorPosition{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var got int64
	if !rows.Next() || rows.Scan(&got) != nil || got != 7 {
		t.Fatal(got, rows.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorResetLifetime(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 71, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(71)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 66, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 71)) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cur, err := OpenCursor(context.Background(), session, query, CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	reused, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reused.Close() }()
	if err = reused.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(context.Background(), CursorPosition{}, 1); !errors.Is(err, sql.ErrConnDone) && !errors.Is(err, ErrResource) {
		t.Fatal("cursor survived reset", err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorMalformedOpenDiscards(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 72, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(72)) || !sendPackets(ctx, t, p, []byte{255}) {
			return
		}
		p.ResetSequence()
		if command, err := p.ReadMessage(ctx); err == nil {
			t.Errorf("malformed response allowed another command: %x", command)
		}
	})
	defer func() { _ = db.Close() }()
	session := pinnedSession(t, db)
	defer func() { _ = session.Close() }()
	if _, err := OpenCursor(context.Background(), session, query, CursorOptions{}); !errors.Is(err, wire.ErrMalformed) {
		t.Fatal(err)
	}
	if err := session.PingContext(context.Background()); err == nil {
		t.Fatal("malformed cursor open left connection usable")
	}
}
