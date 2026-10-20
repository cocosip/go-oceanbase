package lob_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	io "io"
	math "math"
	strings "strings"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	charset "github.com/cocosip/go-oceanbase/internal/charset"
	connect "github.com/cocosip/go-oceanbase/internal/connect"
	lob "github.com/cocosip/go-oceanbase/internal/lob"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestClobMutation(t *testing.T) {
	for _, national := range []bool{false, true} {
		t.Run(map[bool]string{false: "CLOB", true: "NCLOB"}[national], func(t *testing.T) {
			name, collation, inputType, offset, replacement, after, trimmed := "utf8mb4", uint16(45), uint16(253), int64(1), "国", "a国😀z", "a国😀"
			if national {
				name, collation, inputType, offset, replacement, after, trimmed = "utf16", 54, 208, 2, "😁", "a中😁z", "a中😁"
			}
			initialData, _ := charset.Encode(name, "a中😀z")
			afterData, _ := charset.Encode(name, after)
			trimmedData, _ := charset.Encode(name, trimmed)
			locator, updated, short := publictest.LobInlineFixture(string(initialData)), publictest.LobInlineFixture(string(afterData)), publictest.LobInlineFixture(string(trimmedData))
			db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, "SELECT c FROM t") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("c", 211, collation, 0), []byte{254, 0, 0, 6, 0}, append([]byte{byte(len(locator))}, locator...), []byte{254, 0, 0, 6, 0}) {
					return
				}
				params := []wire.Parameter{{Type: 211, Data: locator, LengthEncoded: true}, publictest.LOBInteger(publictest.LOBUnits(replacement, national)), publictest.LOBInteger(offset + 1), {Type: inputType, Data: []byte(replacement), LengthEncoded: true}}
				if !publictest.ServeLOBCall(ctx, t, p, lob.WriteSQL, params, [][]byte{testpeer.TextColumn("c", 211, collation, 0)}, append([]byte{0, 0, byte(len(updated))}, updated...)) {
					return
				}
				params = []wire.Parameter{{Type: 211, Data: updated, LengthEncoded: true}, publictest.LOBInteger(publictest.LOBUnits(trimmed, national))}
				if !publictest.ServeLOBCall(ctx, t, p, publictest.LOBTrimSQL, params, [][]byte{testpeer.TextColumn("c", 211, collation, 0)}, append([]byte{0, 0, byte(len(short))}, short...)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			c, err := oceanbase.OpenClob(context.Background(), pinned, "SELECT c FROM t")
			if err != nil {
				t.Fatal(err)
			}
			if n, err := c.WriteChars(context.Background(), offset, replacement); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			if err = c.TruncateChars(context.Background(), 3); err != nil {
				t.Fatal(err)
			}
			if text, err := c.ReadChars(context.Background(), 0, 3); err != nil || text != trimmed {
				t.Fatal(text, err)
			}
			if size, err := c.CharLength(context.Background()); err != nil || size != 3 {
				t.Fatal(size, err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			_ = pinned.Close()
		})
	}
}

func TestLOBWritePartial(t *testing.T) {
	locator, updated := publictest.LobOutFixture(0), publictest.LobOutFixture(32767)
	value := []byte(strings.Repeat("a", 32768))
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
			return
		}
		params := []wire.Parameter{{Type: 210, Data: locator, LengthEncoded: true}, publictest.LOBInteger(32767), publictest.LOBInteger(1), {Type: 203, Data: value[:32767], LengthEncoded: true}}
		if !publictest.ServeLOBCall(ctx, t, p, lob.WriteSQL, params, [][]byte{testpeer.TextColumn("b", 210, 63, 0)}, append([]byte{0, 0, byte(len(updated))}, updated...)) {
			return
		}
		params = []wire.Parameter{{Type: 210, Data: updated, LengthEncoded: true}, publictest.LOBInteger(1), publictest.LOBInteger(32768), {Type: 203, Data: value[32767:], LengthEncoded: true}}
		command, err := wire.EncodePrepareExecute(0, []byte(lob.WriteSQL), params, 64<<20)
		if err != nil || !testpeer.ReadCommand(ctx, t, p, command) || !testpeer.SendPackets(ctx, t, p, []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	b, err := oceanbase.OpenBlob(context.Background(), pinned, "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	var server *oceanbase.Error
	if n, err := b.WriteAtContext(context.Background(), value, 0); n != 32767 || !errors.As(err, &server) {
		t.Fatal(n, err)
	}
	if size, err := b.Size(context.Background()); err != nil || size != 32767 {
		t.Fatal(size, err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestLOBValidation(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeOracle, config.ModeMySQL} {
		t.Run(map[config.Mode]string{config.ModeOracle: "Oracle", config.ModeMySQL: "MySQL"}[mode], func(t *testing.T) {
			db := publictest.TxDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if mode == config.ModeOracle && !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM t", 210, publictest.LobInlineFixture(publictest.TestLOBABC)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			b, err := oceanbase.OpenBlob(context.Background(), pinned, "SELECT b FROM t")
			if mode == config.ModeMySQL {
				if !errors.Is(err, dberrors.ErrUnsupported) {
					t.Fatal(err)
				}
				_ = pinned.Close()
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = b.ReadAtContext(context.Background(), make([]byte, 2), math.MaxInt64); !errors.Is(err, oceanbase.ErrLOB) {
				t.Fatal(err)
			}
			if _, err = b.WriteAtContext(context.Background(), []byte("x"), -1); !errors.Is(err, oceanbase.ErrLOB) {
				t.Fatal(err)
			}
			if err = b.Truncate(context.Background(), 4); !errors.Is(err, oceanbase.ErrLOB) {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err = b.WriteAtContext(ctx, []byte("x"), 0); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if err = b.Close(); err != nil {
				t.Fatal(err)
			}
			_ = pinned.Close()
		})
	}
}

func TestClobUTF16OverwriteBoundary(t *testing.T) {
	for _, pair := range [][2]string{{"aX", "😀"}, {"😀X", "a"}} {
		t.Run(pair[0], func(t *testing.T) {
			db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				data, _ := charset.Encode("utf16", pair[0])
				if !testpeer.ReadQuery(ctx, t, p, "SELECT c FROM t") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("c", 211, 54, 0), []byte{254, 0, 0, 6, 0}, append([]byte{byte(len(data) + 8)}, publictest.LobInlineFixture(string(data))...), []byte{254, 0, 0, 6, 0}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			c, err := oceanbase.OpenClob(context.Background(), pinned, "SELECT c FROM t")
			if err != nil {
				t.Fatal(err)
			}
			if n, err := c.WriteChars(context.Background(), 0, pair[1]); n != 0 || !errors.Is(err, dberrors.ErrUnsupported) {
				t.Fatal("unsafe surrogate-width overwrite", n, err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			_ = pinned.Close()
		})
	}
}

func TestLOBOracleAmount(t *testing.T) {
	for _, typ := range []byte{3, 246} {
		t.Run(map[byte]string{3: "INTEGER", 246: "NUMBER"}[typ], func(t *testing.T) {
			locator := publictest.LobOutFixture(2)
			db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
					return
				}
				params := []wire.Parameter{publictest.LOBInteger(2), {Type: 210, Data: locator, LengthEncoded: true}, publictest.LOBInteger(1), {Type: 6}, {Type: 6}}
				row := []byte{0, 0, 3, '2', 'e', '0', 2, 'a', 'b'}
				if typ == 3 {
					row = append(binary.LittleEndian.AppendUint32([]byte{0, 0}, 2), 2, 'a', 'b')
				}
				if !publictest.ServeLOBCall(ctx, t, p, lob.ReadBlobSQL, params, [][]byte{testpeer.TextColumn("n", typ, 63, 0), testpeer.TextColumn("v", 203, 63, 0)}, row) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			b, err := oceanbase.OpenBlob(context.Background(), pinned, "SELECT b FROM t")
			if err != nil {
				t.Fatal(err)
			}
			value := make([]byte, 2)
			if n, err := b.ReadAtContext(context.Background(), value, 0); err != nil || n != 2 || string(value) != "ab" {
				t.Fatal(n, value, err)
			}
			if err = b.Close(); err != nil {
				t.Fatal(err)
			}
			_ = pinned.Close()
		})
	}
}

func TestClobOutRow(t *testing.T) {
	locator := publictest.LobOutFixture(9)
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT c FROM t", 211, locator) {
			return
		}
		for pass := 0; pass < 2; pass++ {
			for _, part := range []struct {
				off  uint64
				text string
			}{{1, "a中"}, {3, "😀z"}} {
				params := []wire.Parameter{publictest.LOBInteger(8191), {Type: 211, Data: locator, LengthEncoded: true}, publictest.LobIntFixture(part.off), {Type: 6}, {Type: 6}}
				row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 2)
				row = append(append(row, byte(len(part.text))), part.text...)
				if !publictest.ServeLOBCall(ctx, t, p, lob.ReadClobSQL, params, [][]byte{testpeer.TextColumn("n", 8, 63, 0), testpeer.TextColumn("v", 253, 45, 0)}, row) {
					return
				}
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	c, err := oceanbase.OpenClob(context.Background(), pinned, "SELECT c FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if text, err := c.ReadChars(context.Background(), 1, 2); err != nil || text != "中😀" {
		t.Fatal(text, err)
	}
	if size, err := c.CharLength(context.Background()); err != nil || size != 4 {
		t.Fatal(size, err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestLOBLargeReader(t *testing.T) {
	value := strings.Repeat("a", (16<<20)+1)
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM t", 210, publictest.LobInlineFixture(value)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	b, err := oceanbase.OpenBlob(ctx, pinned, "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	n, err := io.Copy(io.Discard, b.Reader(context.Background()))
	if err != nil || n != int64(len(value)) {
		t.Fatal(n, err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestLOBInterleave(t *testing.T) {
	locator := publictest.LobOutFixture(3)
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT mixed") || !testpeer.SendPackets(ctx, t, p,
			[]byte{1}, testpeer.TextColumn("a", 253, 45, 0), []byte{254, 0, 0, 6, 0}, []byte{1, 'x'}, []byte{254, 0, 0, 14, 0},
			[]byte{1}, testpeer.TextColumn("b", 210, 63, 0), []byte{254, 0, 0, 6, 0}, append([]byte{byte(len(locator))}, locator...), []byte{254, 0, 0, 14, 0},
			[]byte{1}, testpeer.TextColumn("c", 253, 45, 0), []byte{254, 0, 0, 6, 0}, []byte{1, 'z'}, []byte{254, 0, 0, 6, 0}) {
			return
		}
		params := []wire.Parameter{publictest.LOBInteger(3), {Type: 210, Data: locator, LengthEncoded: true}, publictest.LOBInteger(1), {Type: 6}, {Type: 6}}
		row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 3)
		row = append(row, 3, 'a', 'b', 'c')
		if !publictest.ServeLOBCall(ctx, t, p, lob.ReadBlobSQL, params, [][]byte{testpeer.TextColumn("n", 8, 63, 0), testpeer.TextColumn("v", 203, 63, 0)}, row) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SELECT mixed")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for i, want := range []string{"x", publictest.TestLOBABC, "z"} {
		if i > 0 && !rows.NextResultSet() {
			t.Fatal(rows.Err())
		}
		if !rows.Next() {
			t.Fatal(rows.Err())
		}
		var got string
		if err = rows.Scan(&got); err != nil || got != want {
			t.Fatal(got, err)
		}
		if rows.Next() {
			t.Fatal("extra row")
		}
	}
	if rows.NextResultSet() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
}

func TestLOBTemporary(t *testing.T) {
	locator := publictest.LobInlineFixture("x")
	locator[5] |= 1
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
			return
		}
		params := []wire.Parameter{{Type: 210, Data: locator, LengthEncoded: true}, {Type: 6}}
		if !publictest.ServeLOBCall(ctx, t, p, "DECLARE l BLOB := ?; BEGIN IF DBMS_LOB.ISTEMPORARY(l) = 1 THEN DBMS_LOB.FREETEMPORARY(l); END IF; ? := l; END;", params, [][]byte{testpeer.TextColumn("b", 210, 63, 0)}, []byte{0, 4}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	b, err := oceanbase.OpenBlob(context.Background(), pinned, "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestLOBMutation(t *testing.T) {
	locator, updated, trimmed := publictest.LobOutFixture(3), publictest.LobOutFixture(5), publictest.LobOutFixture(2)
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
			return
		}
		params := []wire.Parameter{{Type: 210, Data: locator, LengthEncoded: true}, publictest.LOBInteger(2), publictest.LOBInteger(4), {Type: 203, Data: []byte("de"), LengthEncoded: true}}
		if !publictest.ServeLOBCall(ctx, t, p, "CALL DBMS_LOB.WRITE(?, ?, ?, ?)", params, [][]byte{testpeer.TextColumn("b", 210, 63, 0)}, append([]byte{0, 0, byte(len(updated))}, updated...)) {
			return
		}
		params = []wire.Parameter{{Type: 210, Data: updated, LengthEncoded: true}, publictest.LOBInteger(2)}
		if !publictest.ServeLOBCall(ctx, t, p, "CALL DBMS_LOB.TRIM(?, ?)", params, [][]byte{testpeer.TextColumn("b", 210, 63, 0)}, append([]byte{0, 0, byte(len(trimmed))}, trimmed...)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	b, err := oceanbase.OpenBlob(context.Background(), pinned, "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := b.WriteAtContext(context.Background(), []byte("de"), 3); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if size, err := b.Size(context.Background()); err != nil || size != 5 {
		t.Fatal(size, err)
	}
	if err = b.Truncate(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if size, err := b.Size(context.Background()); err != nil || size != 2 {
		t.Fatal(size, err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestLOBOutRow(t *testing.T) {
	locator := publictest.LobOutFixture(5)
	const readSQL = "DECLARE n INTEGER := ?; v RAW(32767); BEGIN BEGIN DBMS_LOB.READ(?, n, ?, v); EXCEPTION WHEN NO_DATA_FOUND THEN n := 0; END; ? := n; ? := v; END;"
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
			return
		}
		for _, part := range []struct {
			request, offset uint64
			value           string
		}{{5, 1, "ab"}, {3, 3, "cde"}} {
			params := []wire.Parameter{publictest.LobIntFixture(part.request), {Type: 210, Data: locator, LengthEncoded: true}, publictest.LobIntFixture(part.offset), {Type: 6}, {Type: 6}}
			row := binary.LittleEndian.AppendUint64([]byte{0, 0}, uint64(len(part.value)))
			row = append(append(row, byte(len(part.value))), part.value...)
			if !publictest.ServeLOBCall(ctx, t, p, readSQL, params, [][]byte{testpeer.TextColumn("n", 8, 63, 0), testpeer.TextColumn("v", 203, 63, 0)}, row) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	b, err := oceanbase.OpenBlob(context.Background(), pinned, "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 7)
	n, err := b.ReadAtContext(context.Background(), data, 0)
	if n != 5 || err != io.EOF || string(data[:n]) != "abcde" {
		t.Fatal(n, data, err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestClobOffsets(t *testing.T) {
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT c FROM t", 211, publictest.LobInlineFixture("a中😀z")) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	c, err := oceanbase.OpenClob(context.Background(), pinned, "SELECT c FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if text, err := c.ReadChars(context.Background(), 1, 2); err != nil || text != "中😀" {
		t.Fatal(text, err)
	}
	if length, err := c.CharLength(context.Background()); err != nil || length != 4 {
		t.Fatal(length, err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestLOBLocator(t *testing.T) {
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM t", 210, publictest.LobInlineFixture(publictest.TestLOBABC)) {
			return
		}
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM empty_t", 210, publictest.LobInlineFixture("")) || !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM null_t", 210, nil) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	b, err := oceanbase.OpenBlob(context.Background(), pinned, "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 3)
	n, err := b.ReadAtContext(context.Background(), data, 1)
	if n != 2 || err != io.EOF || string(data[:n]) != "bc" {
		t.Fatal(n, data, err)
	}
	if size, err := b.Size(context.Background()); err != nil || size != 3 {
		t.Fatal(size, err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Size(context.Background()); !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("closed BLOB usable", err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	empty, err := oceanbase.OpenBlob(context.Background(), pinned, "SELECT b FROM empty_t")
	if err != nil || empty == nil {
		t.Fatal("empty BLOB lost identity", empty, err)
	}
	if err = empty.Close(); err != nil {
		t.Fatal(err)
	}
	null, err := oceanbase.OpenBlob(context.Background(), pinned, "SELECT b FROM null_t")
	if err != nil || null != nil {
		t.Fatal("NULL is not an empty handle", null, err)
	}
	_ = pinned.Close()
}

func TestLOBHandshake(t *testing.T) {
	caps, err := connect.ClientCapabilities(config.Config{}, wire.ClientProtocol41|wire.ClientSecureConnection|wire.ClientConnectAttrs)
	if err != nil || caps&(1<<29) == 0 || caps&wire.ClientConnectAttrs == 0 {
		t.Fatal(caps, err)
	}
}

func TestLOBSession(t *testing.T) {
	var nilConn *sql.Conn
	var nilTx *sql.Tx
	wrapper := &publictest.ResourceSessionWrapper{}
	for _, session := range []oceanbase.Session{nil, nilConn, nilTx, new(sql.DB), wrapper} {
		if _, err := oceanbase.OpenBlob(context.Background(), session, "SELECT b FROM t"); !errors.Is(err, oceanbase.ErrResource) {
			t.Fatalf("%T: %v", session, err)
		}
		if _, err := oceanbase.OpenClob(context.Background(), session, "SELECT c FROM t"); !errors.Is(err, oceanbase.ErrResource) {
			t.Fatalf("%T: %v", session, err)
		}
	}
	if wrapper.Calls.Load() != 0 {
		t.Fatal("invalid Session was dispatched")
	}
}
