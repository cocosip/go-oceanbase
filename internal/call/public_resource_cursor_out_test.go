package call_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	binary "encoding/binary"
	errors "errors"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	lob "github.com/cocosip/go-oceanbase/internal/lob"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestOutputLeaseTransaction(t *testing.T) {
	db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) {
			return
		}
		want, _ := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 4096)
		if !testpeer.ReadCommand(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(91, 1, 1, 4), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 5, 0}, testpeer.TextColumn("a", 163, 63, 0), []byte{254, 0, 0, 5, 16}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 5, 0}, []byte{0, 0, 0, 5, 0, 0, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 91)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tr, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	var cur *oceanbase.Cursor
	e, err := oceanbase.Execute(ctx, tr, "CALL p(?)", oceanbase.CursorOut{Dest: &cur})
	if err != nil || e.Close() != nil || cur == nil {
		t.Fatal(err, cur)
	}
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(ctx, oceanbase.CursorPosition{}, 1); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("output cursor survived transaction", err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorOutCapability(t *testing.T) {
	for _, tc := range []struct {
		mode    config.Mode
		version string
	}{{config.ModeMySQL, testOutVersion}, {config.ModeOracle, "2.2.75"}, {config.ModeOracle, ""}} {
		db := publictest.QueryVersionDB(t, tc.version, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); testpeer.ServeQuit(t, p) }, func(cfg *config.Config) { cfg.Mode = tc.mode })
		pinned := publictest.PinnedSession(t, db)
		var cur *oceanbase.Cursor
		if _, err := oceanbase.Execute(context.Background(), pinned, "CALL p(?)", oceanbase.CursorOut{Dest: &cur}); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal(err)
		}
		if err := pinned.Close(); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCursorOutMalformed(t *testing.T) {
	db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		want, _ := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 4096)
		if !testpeer.ReadCommand(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(91, 1, 1, 4), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("a", 163, 63, 0), []byte{254, 0, 0, 6, 16}, []byte{0, 0, 92, 0}) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("malformed output retained physical connection")
		}
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var cur *oceanbase.Cursor
	_, err = oceanbase.Execute(ctx, s, "CALL p(?)", oceanbase.CursorOut{Dest: &cur})
	if !errors.Is(err, wire.ErrMalformed) || cur != nil {
		t.Fatal(err, cur)
	}
}

func TestCursorOutTailFailure(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		t.Run(map[bool]string{false: "outputs-only", true: "ordinary-first"}[ordinary], func(t *testing.T) {
			db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				if !serveResourceOut(ctx, t, p, "CALL p(?)", [][]byte{testpeer.TextColumn("a", 163, 63, 0)}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), ordinary, true) {
					return
				}
				if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 91)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			var cur *oceanbase.Cursor
			e, err := oceanbase.Execute(ctx, s, "CALL p(?)", oceanbase.CursorOut{Dest: &cur})
			if ordinary && err != nil {
				t.Fatal(err)
			}
			if e != nil {
				err = e.Close()
			}
			var server *oceanbase.Error
			if !errors.As(err, &server) || cur != nil {
				t.Fatal("tail error published handle", err, cur)
			}
			if err = s.PingContext(ctx); err != nil {
				t.Fatal("complete server error discarded session", err)
			}
		})
	}
}

func TestCursorOutCancelDuringConversion(t *testing.T) {
	db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		row := binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), 7)
		if !serveResourceOut(ctx, t, p, "CALL p(?,?)", [][]byte{testpeer.TextColumn("a", 163, 63, 0), testpeer.TextColumn("b", 8, 63, 0)}, row, false, false) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 91)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	callCtx, cancelCall := context.WithCancel(ctx)
	defer cancelCall()
	var cur *oceanbase.Cursor
	e, err := oceanbase.Execute(callCtx, s, "CALL p(?,?)", oceanbase.CursorOut{Dest: &cur}, oceanbase.Out{Dest: &callbackOutScanner{scan: cancelCall}, Type: "BIGINT"})
	if e != nil {
		_ = e.Close()
	}
	if !errors.Is(err, context.Canceled) || cur != nil {
		t.Fatal("published canceled outputs", err, cur)
	}
}

func TestCursorOutPreSend(t *testing.T) {
	var cur *oceanbase.Cursor
	var blob *oceanbase.Blob
	var clob *oceanbase.Clob
	occupied := &oceanbase.Cursor{}
	for _, arg := range []any{oceanbase.CursorOut{}, oceanbase.CursorOut{Dest: &occupied}, oceanbase.Out{Dest: &blob}, sql.Out{Dest: &blob}, oceanbase.Out{Dest: &blob, Type: valueformat.TypeNameClob}, oceanbase.Out{Dest: &clob, Type: valueformat.TypeNameClob, In: true}, oceanbase.Out{Dest: &blob, Type: valueformat.TypeNameBlob, Length: 1}} {
		if _, err := normalizeArgument(t, arg); !errors.Is(err, oceanbase.ErrOut) {
			t.Fatalf("%T: %v", arg, err)
		}
	}
	for _, arg := range []any{oceanbase.CursorOut{Dest: &cur}, oceanbase.Out{Dest: &blob, Type: valueformat.TypeNameBlob}, oceanbase.Out{Dest: &clob, Type: valueformat.TypeNameNClob}} {
		for _, query := range []bool{false, true} {
			c := outputTestConn(t, 0, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); testpeer.ServeQuit(t, p) })
			var err error
			args := []driver.NamedValue{{Ordinal: 1, Value: arg}}
			if query {
				_, err = c.QueryContext(context.Background(), "CALL p(?)", args)
			} else {
				_, err = c.ExecContext(context.Background(), "CALL p(?)", args)
			}
			if !errors.Is(err, oceanbase.ErrOut) {
				t.Fatal(err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestOutputLOBHandles(t *testing.T) {
	for _, null := range []bool{false, true} {
		t.Run(map[bool]string{false: "streaming", true: "sql-null"}[null], func(t *testing.T) {
			locator := publictest.LobOutFixture(1 << 32)
			db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				row := []byte{0, 28}
				if !null {
					row = binary.LittleEndian.AppendUint32([]byte{0, 0}, 92)
					row = append(append(row, byte(len(locator))), locator...)
					clob := publictest.LobInlineFixture("你好")
					row = append(append(row, byte(len(clob))), clob...)
				}
				if !serveResourceOut(ctx, t, p, "CALL p(?,?,?)", [][]byte{testpeer.TextColumn("a", 163, 63, 0), testpeer.TextColumn("b", 210, 63, 0), testpeer.TextColumn("c", 211, 45, 0)}, row, false, false) {
					return
				}
				if !null {
					params := []wire.Parameter{publictest.LobIntFixture(3), {Type: 210, Data: locator, LengthEncoded: true}, publictest.LobIntFixture(1), {Type: 6}, {Type: 6}}
					result := append(binary.LittleEndian.AppendUint64([]byte{0, 0}, 3), 3, 'a', 'b', 'c')
					if !publictest.ServeLOBCall(ctx, t, p, lob.ReadBlobSQL, params, [][]byte{testpeer.TextColumn("n", 8, 63, 0), testpeer.TextColumn("v", 203, 63, 0)}, result) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
						return
					}
				}
				if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 91)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			var cur *oceanbase.Cursor
			var blob *oceanbase.Blob
			var clob *oceanbase.Clob
			e, err := oceanbase.Execute(ctx, s, "CALL p(?,?,?)", oceanbase.CursorOut{Dest: &cur}, oceanbase.Out{Dest: &blob, Type: valueformat.TypeNameBlob}, oceanbase.Out{Dest: &clob, Type: valueformat.TypeNameClob})
			if err != nil || e.Close() != nil {
				t.Fatal(err)
			}
			if null {
				if cur != nil || blob != nil || clob != nil {
					t.Fatal("NULL produced handles")
				}
				return
			}
			if cur == nil || blob == nil || clob == nil {
				t.Fatal("missing mixed handles")
			}
			if size, err := blob.Size(ctx); err != nil || size != 1<<32 {
				t.Fatal(size, err)
			}
			data := make([]byte, 3)
			if n, err := blob.ReadAtContext(ctx, data, 0); err != nil || n != 3 || string(data) != "abc" {
				t.Fatal(n, string(data), err)
			}
			if text, err := clob.ReadChars(ctx, 0, 2); err != nil || text != "你好" {
				t.Fatal(text, err)
			}
			if err = blob.Close(); err != nil {
				t.Fatal(err)
			}
			if err = cur.Close(); err != nil {
				t.Fatal(err)
			}
			if err = s.PingContext(ctx); err != nil {
				t.Fatal("parent closed with a clob lease", err)
			}
			if err = clob.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOutputLease(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "multiple", true: "scanner-failure"}[fail], func(t *testing.T) {
			db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				row := binary.LittleEndian.AppendUint32([]byte{0, 0}, 92)
				row = binary.LittleEndian.AppendUint32(row, 93)
				row = binary.LittleEndian.AppendUint64(row, 7)
				if !serveResourceOut(ctx, t, p, "CALL p(?,?,?)", [][]byte{testpeer.TextColumn("a", 163, 63, 0), testpeer.TextColumn("b", 163, 63, 0), testpeer.TextColumn("c", 8, 63, 0)}, row, false, false) {
					return
				}
				ids := map[uint32]bool{}
				for range 3 {
					p.ResetSequence()
					b, err := p.ReadMessage(ctx)
					if err != nil || len(b) != 5 || b[0] != 25 {
						t.Error("expected cursor/parent close", b, err)
						return
					}
					id := binary.LittleEndian.Uint32(b[1:])
					if ids[id] || id < 91 || id > 93 || len(ids) < 2 && id == 91 {
						t.Error("lease close order", ids, id)
						return
					}
					ids[id] = true
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			var a, b *oceanbase.Cursor
			var n int64
			var dest any = &n
			scanner := &failingOutScanner{}
			if fail {
				dest = scanner
			}
			e, err := oceanbase.Execute(ctx, s, "CALL p(?,?,?)", oceanbase.CursorOut{Dest: &a}, oceanbase.CursorOut{Dest: &b}, oceanbase.Out{Dest: dest, Type: "BIGINT"})
			if fail {
				if !errors.Is(err, oceanbase.ErrOut) || a != nil || b != nil || scanner.calls != 1 {
					t.Fatal(err, a, b, scanner.calls)
				}
				return
			}
			if err != nil || e.Close() != nil || a == nil || b == nil || n != 7 {
				t.Fatal(err, a, b, n)
			}
			if err = a.Close(); err != nil {
				t.Fatal(err)
			}
			if err = b.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorOutPublish(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{true: "early-close", false: "consume"}[early], func(t *testing.T) {
			db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				if !serveResourceOut(ctx, t, p, "CALL p(?)", [][]byte{testpeer.TextColumn("cursor", 163, 63, 0)}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), true, false) {
					return
				}
				if !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(92, 2)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("value", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 134, 0}) {
					return
				}
				if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 91)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			old, cancelOld := context.WithCancel(ctx)
			var cur *oceanbase.Cursor
			e, err := oceanbase.Execute(old, s, "CALL p(?)", oceanbase.CursorOut{Dest: &cur})
			if err != nil || cur != nil {
				t.Fatal(err, cur)
			}
			if !early {
				for e.Rows().Next() {
				}
			}
			if err = e.Close(); err != nil || cur == nil {
				t.Fatal(err, cur)
			}
			cancelOld()
			rows, err := cur.Fetch(ctx, oceanbase.CursorPosition{}, 2)
			if err != nil {
				t.Fatal(err)
			}
			var n int64
			if !rows.Next() || rows.Scan(&n) != nil || n != 42 {
				t.Fatal(n, rows.Err())
			}
			if err = rows.Close(); err != nil {
				t.Fatal(err)
			}
			if err = cur.Close(); err != nil {
				t.Fatal(err)
			}
			if err = s.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
