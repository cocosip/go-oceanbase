package cursor_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	reflect "reflect"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCursorMalformedOpenDiscards(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 72, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(72)) || !testpeer.SendPackets(ctx, t, p, []byte{255}) {
			return
		}
		p.ResetSequence()
		if command, err := p.ReadMessage(ctx); err == nil {
			t.Errorf("malformed response allowed another command: %x", command)
		}
	})
	defer func() { _ = db.Close() }()
	session := publictest.PinnedSession(t, db)
	defer func() { _ = session.Close() }()
	if _, err := oceanbase.OpenCursor(context.Background(), session, query, oceanbase.CursorOptions{}); !errors.Is(err, wire.ErrMalformed) {
		t.Fatal(err)
	}
	if err := session.PingContext(context.Background()); err == nil {
		t.Fatal("malformed cursor open left connection usable")
	}
}

func TestCursorResetLifetime(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 71, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(71)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 66, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 71)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cur, err := oceanbase.OpenCursor(context.Background(), session, query, oceanbase.CursorOptions{})
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
	if _, err = cur.Fetch(context.Background(), oceanbase.CursorPosition{}, 1); !errors.Is(err, sql.ErrConnDone) && !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("cursor survived reset", err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorOldOracleFetch(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := publictest.QueryVersionDB(t, "2.2.4", func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 68, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(68)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 70, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(68, 1)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), []byte{254, 0, 0, 134, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 68)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	}, func(cfg *config.Config) { cfg.Mode = config.ModeOracle })
	defer func() { _ = db.Close() }()
	session := publictest.PinnedSession(t, db)
	defer func() { _ = session.Close() }()
	if _, err := oceanbase.OpenCursor(context.Background(), session, query, oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive}); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
	cur, err := oceanbase.OpenCursor(context.Background(), session, query, oceanbase.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(context.Background(), oceanbase.CursorPosition{}, 1)
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

func TestCursorDeprecatedEOF(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := publictest.CapabilityDB(t, config.Config{Mode: config.ModeMySQL}, wire.ClientDeprecateEOF, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 67, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(67)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0)) || !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(67, 1)) || !testpeer.SendPackets(ctx, t, p, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), []byte{254, 0, 0, 130, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 67)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := publictest.PinnedSession(t, db)
	defer func() { _ = session.Close() }()
	cur, err := oceanbase.OpenCursor(context.Background(), session, query, oceanbase.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(context.Background(), oceanbase.CursorPosition{}, 1)
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

func TestCursorTxLifetime(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !publictest.ServePrepare(ctx, t, p, query, 60, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(60)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 67, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(60, 1)) || !testpeer.SendPackets(ctx, t, p, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), []byte{254, 0, 0, 131, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 60)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}) {
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
	openCtx, cancel := context.WithCancel(context.Background())
	cur, err := oceanbase.OpenCursor(openCtx, tr, query, oceanbase.CursorOptions{})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(context.Background(), oceanbase.CursorPosition{}, 1)
	if err != nil {
		t.Fatal("fetch inherited open context", err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(context.Background(), oceanbase.CursorPosition{}, 1); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("cursor survived transaction", err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorFetchFailureInvalidates(t *testing.T) {
	const query = "SELECT id FROM t"
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 59, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(59)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 66, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(59, 1)) || !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 66, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 59)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := publictest.PinnedSession(t, db)
	defer func() { _ = session.Close() }()
	cur, err := oceanbase.OpenCursor(context.Background(), session, query, oceanbase.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(context.Background(), oceanbase.CursorPosition{}, 1); !errors.Is(err, oceanbase.ErrCursor) {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(context.Background(), oceanbase.CursorPosition{}, 1); !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("failed fetch left cursor reusable", err)
	}
	if err = session.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCursorRejectsUnsafeOpen(t *testing.T) {
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := publictest.PinnedSession(t, db)
	defer func() { _ = session.Close() }()
	for _, tc := range []struct {
		query   string
		options oceanbase.CursorOptions
		want    error
	}{
		{"SELECT id FROM t; DELETE FROM t", oceanbase.CursorOptions{}, oceanbase.ErrCursor},
		{"SELECT id FROM t", oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive}, dberrors.ErrUnsupported},
		{"SELECT id FROM t", oceanbase.CursorOptions{Updatable: true}, dberrors.ErrUnsupported},
	} {
		if _, err := oceanbase.OpenCursor(context.Background(), session, tc.query, tc.options); !errors.Is(err, tc.want) {
			t.Fatal(tc.query, err)
		}
	}
	if err := session.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCursorEmptyOpen(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 55, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(55)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 130, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 55)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := publictest.PinnedSession(t, db)
	defer func() { _ = session.Close() }()
	cur, err := oceanbase.OpenCursor(context.Background(), session, query, oceanbase.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(context.Background(), oceanbase.CursorPosition{}, 1)
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

func TestCursorSensitive(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	const native = "SELECT id, ROWID FROM cursor_fixture"
	const refresh = `SELECT "id" FROM "s"."t" WHERE ROWID = ?`
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, native, 54, 0, nil) {
			return
		}
		payload, err := wire.EncodePrepareExecute(54, []byte(native), nil, 4096)
		if err != nil {
			t.Error(err)
			return
		}
		payload[5] = 1
		binary.LittleEndian.PutUint32(payload[len(payload)-16:], 8)
		old := append(binary.LittleEndian.AppendUint64([]byte{0, 0}, 1), 4, 'A', 'A', 'A', 'A')
		if !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(54, 2, 0, 0), cursorBaseColumn("id", 8), testpeer.TextColumn("ROWID", 253, 63, 0), []byte{254, 0, 0, 70, 0}, old, []byte{254, 0, 0, 70, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) {
			return
		}
		for i, value := range []uint64{7, 9} {
			fetch := append(publictest.CursorFetchFixture(54, 1), 32, 0, 1, 0, 0, 0, 1, 0, 0, 0)
			if !testpeer.ReadCommand(ctx, t, p, fetch) || !testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), testpeer.TextColumn("ROWID", 253, 63, 0), []byte{254, 0, 0, 70, 0}, old, []byte{254, 0, 0, 134, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) {
				t.Errorf("sensitive fetch %d", i)
				return
			}
			if !publictest.ServePrepare(ctx, t, p, refresh, 55, 1, nil) || !testpeer.ReadCommand(ctx, t, p, append(publictest.ExecuteHeader(55), 0, 1, 253, 0, 4, 'A', 'A', 'A', 'A')) || !testpeer.SendPackets(ctx, t, p, []byte{1}, cursorBaseColumn("id", 8), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, value), []byte{254, 0, 0, 6, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 55)) {
				return
			}
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 54)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session := publictest.PinnedSession(t, db)
	defer func() { _ = session.Close() }()
	cur, err := oceanbase.OpenCursor(context.Background(), session, query, oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{7, 9} {
		rows, e := cur.Fetch(context.Background(), oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 1}, 1)
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

func TestCursorOracleFetchLOB(t *testing.T) {
	const query = "SELECT payload FROM cursor_fixture"
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 53, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(53)) ||
			!testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("payload", 210, 63, 0), []byte{254, 0, 0, 70, 0}) {
			return
		}
		fetch := append(publictest.CursorFetchFixture(53, 1), 2, 0, 0, 0, 0, 0, 1, 0, 0, 0)
		if !testpeer.ReadCommand(ctx, t, p, fetch) || !testpeer.SendPackets(ctx, t, p,
			[]byte{1}, testpeer.TextColumn("payload", 210, 63, 0), []byte{254, 0, 0, 6, 0},
			wire.AppendComplexLenBytes([]byte{0, 0}, publictest.LobInlineFixture(publictest.TestLOBABC)), []byte{254, 0, 0, 134, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 53)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	cur, err := oceanbase.OpenCursor(ctx, session, query, oceanbase.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(ctx, oceanbase.CursorPosition{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	if !rows.Next() || rows.Scan(&got) != nil || string(got) != publictest.TestLOBABC {
		t.Fatal(got, rows.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorStatic(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	for _, limit := range []int64{0, 180} {
		t.Run(map[bool]string{false: "scroll", true: "budget"}[limit != 0], func(t *testing.T) {
			db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServePrepare(ctx, t, p, query, 52, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(52)) ||
					!testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 66, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(52, 128)) {
					return
				}
				for i := uint64(1); i <= 3; i++ {
					if !testpeer.SendPackets(ctx, t, p, binary.LittleEndian.AppendUint64([]byte{0, 0}, i)) {
						return
					}
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 130, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 52)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			session, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			cur, err := oceanbase.OpenCursor(ctx, session, query, oceanbase.CursorOptions{Mode: oceanbase.CursorStatic, MaxCacheBytes: limit})
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
				pos   oceanbase.CursorPosition
				count int
				want  []int64
			}{{oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 2}, 1, []int64{2}}, {oceanbase.CursorPosition{Origin: oceanbase.CursorRelative, Offset: -1}, 2, []int64{1, 2}}, {oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 0}, 1, nil}, {oceanbase.CursorPosition{}, 1, []int64{1}}, {oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 99}, 1, nil}} {
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

func TestCursorPreSendValidation(t *testing.T) {
	for _, opts := range []oceanbase.CursorOptions{{Mode: 255}, {MaxCacheBytes: -1}} {
		if _, err := oceanbase.OpenCursor(context.Background(), new(sql.Conn), "SELECT 1", opts); !errors.Is(err, oceanbase.ErrCursor) {
			t.Fatal(err)
		}
	}
	if _, err := oceanbase.OpenCursor(context.Background(), new(sql.DB), "SELECT 1", oceanbase.CursorOptions{}); !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal(err)
	}
}

func TestCursorFetch(t *testing.T) {
	const query = "SELECT id FROM cursor_fixture"
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 51, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(51)) ||
			!testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 66, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(51, 2)) || !testpeer.SendPackets(ctx, t, p,
			binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), binary.LittleEndian.AppendUint64([]byte{0, 0}, 8), []byte{254, 0, 0, 130, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 51)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	cur, err := oceanbase.OpenCursor(ctx, session, query, oceanbase.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cur.Fetch(ctx, oceanbase.CursorPosition{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(ctx, oceanbase.CursorPosition{}, 1); !errors.Is(err, oceanbase.ErrCursorBusy) {
		t.Fatal("unclosed batch allowed a second fetch", err)
	}
	if err = cur.Close(); !errors.Is(err, oceanbase.ErrCursorBusy) {
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
	rows, err = cur.Fetch(ctx, oceanbase.CursorPosition{}, 1)
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
