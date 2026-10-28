package cursor_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	strings "strings"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCursorFetchChildBudget(t *testing.T) {
	col := testpeer.TextColumn("child", 163, 63, 0)
	col = append(wire.AppendComplexLenBytes(nil, []byte(strings.Repeat("c", 900))), col[4:]...)
	childCol := append(wire.AppendComplexLenBytes(nil, []byte(strings.Repeat("c", 900))), testpeer.TextColumn("n", 8, 63, 0)[4:]...)
	db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, "SELECT child FROM t", 91, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(91)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, col, []byte{254, 0, 0, 70, 0}) {
			return
		}
		fetch := append(publictest.CursorFetchFixture(91, 1), 2, 0, 0, 0, 0, 0, 1, 0, 0, 0)
		if !testpeer.ReadCommand(ctx, t, p, fetch) || !testpeer.SendPackets(ctx, t, p, []byte{1}, col, []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 134, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(92, 128)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, childCol, []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 134, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) {
			return
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	}, func(cfg *config.Config) { cfg.Limits.MaxMessageBytes = 1800 })
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cur, err := oceanbase.OpenCursor(ctx, s, "SELECT child FROM t", oceanbase.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cur.Close() }()
	rows, err := cur.Fetch(ctx, oceanbase.CursorPosition{}, 1)
	if rows != nil {
		_ = rows.Close()
	}
	if !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("parent metadata bypassed shared fetch budget", err)
	}
}

func TestCursorStaticChildren(t *testing.T) {
	for _, budget := range []bool{false, true} {
		t.Run(map[bool]string{false: "rewind", true: "cross-batch-budget"}[budget], func(t *testing.T) {
			db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServePrepare(ctx, t, p, "SELECT child FROM t", 91, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(91)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("child", 163, 63, 0), []byte{254, 0, 0, 70, 0}) {
					return
				}
				batches := 1
				if budget {
					batches = 2
				}
				for i := 0; i < batches; i++ {
					last := byte(134)
					if budget && i == 0 {
						last = 70
					}
					fetch := append(publictest.CursorFetchFixture(91, 1), 2, 0, 0, 0, 0, 0, 1, 0, 0, 0)
					if !testpeer.ReadCommand(ctx, t, p, fetch) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("child", 163, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, uint32(92+i)), []byte{254, 0, 0, last, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) {
						return
					}
					if !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(uint32(92+i), 1)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("n", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 134, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, uint32(92+i))) {
						return
					}
				}
				if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 91)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			}, func(cfg *config.Config) { cfg.Limits.FetchSize = 1 })
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			opts := oceanbase.CursorOptions{Mode: oceanbase.CursorStatic}
			if budget {
				opts.MaxCacheBytes = 600
			}
			openCtx, cancelOpen := context.WithCancel(ctx)
			defer cancelOpen()
			cur, err := oceanbase.OpenCursor(openCtx, s, "SELECT child FROM t", opts)
			if budget {
				if cur != nil {
					_ = cur.Close()
				}
				if !errors.Is(err, wire.ErrMessageTooLarge) {
					t.Fatal("nested snapshots bypassed total cache limit", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cur.Close() }()
			for i := 0; i < 2; i++ {
				rows, err := cur.Fetch(ctx, oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 1}, 1)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = rows.Close() }()
				var child sql.Rows
				var n int64
				if !rows.Next() || rows.Scan(&child) != nil || !child.Next() || child.Scan(&n) != nil || n != 42 {
					t.Fatal("static child was reused or inherited Open context", i, n, child.Err())
				}
				if err = rows.Close(); err != nil {
					t.Fatal(err)
				}
				cancelOpen()
			}
		})
	}
}

func TestCursorSensitiveInitialChildren(t *testing.T) {
	const (
		query      = "SELECT child FROM cursor_fixture"
		native     = "SELECT child, ROWID FROM cursor_fixture"
		nullChild  = "null-child"
		parent     = "parent"
		fetchError = "fetch-error"
	)
	for _, kind := range []string{"child", nullChild, parent, fetchError} {
		t.Run(kind, func(t *testing.T) {
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
				row := append(binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), 4, 'A', 'A', 'A', 'A')
				switch kind {
				case nullChild:
					row = []byte{0, 4, 4, 'A', 'A', 'A', 'A'}
				case parent:
					row = append(binary.LittleEndian.AppendUint32([]byte{0, 0}, 54), 4, 'A', 'A', 'A', 'A')
				}
				if !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(54, 2, 0, 0), testpeer.TextColumn("child", 163, 63, 0), testpeer.TextColumn("ROWID", 253, 63, 0), []byte{254, 0, 0, 70, 0}, row, []byte{254, 0, 0, 70, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) {
					return
				}
				if kind == parent {
					p.ResetSequence()
					if _, err := p.ReadMessage(ctx); err == nil {
						t.Error("parent reference retained the connection or sent another command")
					}
					return
				}
				if kind != nullChild {
					if !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(92, 128)) {
						return
					}
					if kind == fetchError {
						serveFailedInitialChildFetch(ctx, t, p)
						return
					}
					if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("n", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 134, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) {
						return
					}
				}
				if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 54)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
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
			cur, err := oceanbase.OpenCursor(ctx, session, query, oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive})
			switch kind {
			case parent:
				if cur != nil || !errors.Is(err, oceanbase.ErrCursor) {
					t.Fatal("parent reference accepted", cur, err)
				}
				return
			case fetchError:
				var serverError *oceanbase.Error
				if cur != nil || !errors.As(err, &serverError) || serverError.Code != 1064 {
					t.Fatal("child failure lost", cur, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = cur.Close(); err != nil {
				t.Fatal(err)
			}
			if err = session.PingContext(ctx); err != nil {
				t.Fatal("initial child cleanup damaged parent session", err)
			}
		})
	}
}

func serveFailedInitialChildFetch(ctx context.Context, t *testing.T, p *wire.PacketIO) {
	t.Helper()
	if !testpeer.SendPackets(ctx, t, p, []byte{255, 40, 4, '#', 'H', 'Y', '0', '0', '0', 'x'}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) {
		return
	}
	p.ResetSequence()
	if _, err := p.ReadMessage(ctx); err == nil {
		t.Error("failed child fetch retained the connection")
	}
}
