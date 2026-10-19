package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testLOBABC = "abc"

func lobInlineFixture(value string) []byte {
	return append([]byte{'!', 'B', 'O', 'L', 2, 0xa0, 0, 0}, value...)
}

func TestLOBSession(t *testing.T) {
	var nilConn *sql.Conn
	var nilTx *sql.Tx
	wrapper := &resourceSessionWrapper{}
	for _, session := range []Session{nil, nilConn, nilTx, new(sql.DB), wrapper} {
		if _, err := OpenBlob(context.Background(), session, "SELECT b FROM t"); !errors.Is(err, ErrResource) {
			t.Fatalf("%T: %v", session, err)
		}
		if _, err := OpenClob(context.Background(), session, "SELECT c FROM t"); !errors.Is(err, ErrResource) {
			t.Fatalf("%T: %v", session, err)
		}
	}
	if wrapper.calls.Load() != 0 {
		t.Fatal("invalid Session was dispatched")
	}
}

func serveLOBQuery(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, typ byte, values ...[]byte) bool {
	return serveLOBQueryStatus(ctx, t, p, query, typ, 6, values...)
}

func serveLOBQueryStatus(ctx context.Context, t testing.TB, p *wire.PacketIO, query string, typ, status byte, values ...[]byte) bool {
	t.Helper()
	if !readQuery(ctx, t, p, query) || !sendPackets(ctx, t, p, []byte{1}, textColumn("lob", typ, 45, 0), []byte{254, 0, 0, status, 0}) {
		return false
	}
	for _, value := range values {
		row := []byte{251}
		if value != nil {
			row = append(lobLengthPrefix(len(value)), value...)
		}
		if !sendPackets(ctx, t, p, row) {
			return false
		}
	}
	return sendPackets(ctx, t, p, []byte{254, 0, 0, status, 0})
}

func lobLengthPrefix(n int) []byte {
	if n < 251 {
		return []byte{byte(n)}
	}
	if n <= 65535 {
		return []byte{252, byte(n), byte(n >> 8)}
	}
	if n <= 0xffffff {
		return []byte{253, byte(n), byte(n >> 8), byte(n >> 16)}
	}
	return binary.LittleEndian.AppendUint64([]byte{254}, uint64(n))
}

func TestLOBHandshake(t *testing.T) {
	c := &connector{}
	caps, err := c.clientCapabilities(wire.ClientProtocol41 | wire.ClientSecureConnection | wire.ClientConnectAttrs)
	if err != nil || caps&(1<<29) == 0 || caps&wire.ClientConnectAttrs == 0 {
		t.Fatal(caps, err)
	}
}

func TestLOBLocator(t *testing.T) {
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBQuery(ctx, t, p, "SELECT b FROM t", 210, lobInlineFixture(testLOBABC)) {
			return
		}
		if !serveLOBQuery(ctx, t, p, "SELECT b FROM empty_t", 210, lobInlineFixture("")) || !serveLOBQuery(ctx, t, p, "SELECT b FROM null_t", 210, nil) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	b, err := OpenBlob(context.Background(), pinned, "SELECT b FROM t")
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
	if _, err = b.Size(context.Background()); !errors.Is(err, ErrResource) {
		t.Fatal("closed BLOB usable", err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	empty, err := OpenBlob(context.Background(), pinned, "SELECT b FROM empty_t")
	if err != nil || empty == nil {
		t.Fatal("empty BLOB lost identity", empty, err)
	}
	if err = empty.Close(); err != nil {
		t.Fatal(err)
	}
	null, err := OpenBlob(context.Background(), pinned, "SELECT b FROM null_t")
	if err != nil || null != nil {
		t.Fatal("NULL is not an empty handle", null, err)
	}
	_ = pinned.Close()
}

func TestClobOffsets(t *testing.T) {
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBQuery(ctx, t, p, "SELECT c FROM t", 211, lobInlineFixture("a中😀z")) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	c, err := OpenClob(context.Background(), pinned, "SELECT c FROM t")
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

func lobOutFixture(length uint64) []byte {
	data := make([]byte, 36)
	copy(data, []byte{'!', 'B', 'O', 'L', 2})
	data[9] = 1
	binary.LittleEndian.PutUint64(data[28:], length)
	return data
}

func lobIntFixture(n uint64) wire.Parameter {
	return wire.Parameter{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, n)}
}

func serveLOBCall(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, params []wire.Parameter, columns [][]byte, row []byte) bool {
	return serveLOBCallStatus(ctx, t, p, query, params, columns, row, 6)
}

func serveLOBCallStatus(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, params []wire.Parameter, columns [][]byte, row []byte, status byte) bool {
	t.Helper()
	command, err := wire.EncodePrepareExecute(0, []byte(query), params, 64<<20)
	if err != nil || !readCommand(ctx, t, p, command) {
		return false
	}
	packets := [][]byte{combinedHeader(41, uint16(len(columns)), uint16(len(params)), 4)}
	for _, param := range params {
		packets = append(packets, textColumn("p", byte(param.Type), 63, 0))
	}
	packets = append(packets, []byte{254, 0, 0, status, 0})
	packets = append(packets, columns...)
	packets = append(packets, []byte{254, 0, 0, status, 16}, row, []byte{254, 0, 0, status, 0}, []byte{0, 0, 0, status, 0, 0, 0})
	return sendPackets(ctx, t, p, packets...) && readCommand(ctx, t, p, stmtPacket(25, 41))
}

func TestLOBOutRow(t *testing.T) {
	locator := lobOutFixture(5)
	const readSQL = "DECLARE n INTEGER := ?; v RAW(32767); BEGIN BEGIN DBMS_LOB.READ(?, n, ?, v); EXCEPTION WHEN NO_DATA_FOUND THEN n := 0; END; ? := n; ? := v; END;"
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
			return
		}
		for _, part := range []struct {
			request, offset uint64
			value           string
		}{{5, 1, "ab"}, {3, 3, "cde"}} {
			params := []wire.Parameter{lobIntFixture(part.request), {Type: 210, Data: locator, LengthEncoded: true}, lobIntFixture(part.offset), {Type: 6}, {Type: 6}}
			row := binary.LittleEndian.AppendUint64([]byte{0, 0}, uint64(len(part.value)))
			row = append(append(row, byte(len(part.value))), part.value...)
			if !serveLOBCall(ctx, t, p, readSQL, params, [][]byte{textColumn("n", 8, 63, 0), textColumn("v", 203, 63, 0)}, row) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	b, err := OpenBlob(context.Background(), pinned, "SELECT b FROM t")
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

func TestLOBNoReentry(t *testing.T) {
	locator := lobOutFixture(3)
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
			return
		}
		params := []wire.Parameter{lobIntFixture(3), {Type: 210, Data: locator, LengthEncoded: true}, lobIntFixture(1), {Type: 6}, {Type: 6}}
		row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 3)
		row = append(row, 3, 'a', 'b', 'c')
		if !serveLOBCall(ctx, t, p, lobReadBlobSQL, params, [][]byte{textColumn("n", 8, 63, 0), textColumn("v", 203, 63, 0)}, row) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	var value []byte
	if err = rows.Scan(&value); err != nil || string(value) != testLOBABC {
		t.Fatal(value, err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
}

func TestLOBMutation(t *testing.T) {
	locator, updated, trimmed := lobOutFixture(3), lobOutFixture(5), lobOutFixture(2)
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
			return
		}
		params := []wire.Parameter{{Type: 210, Data: locator, LengthEncoded: true}, lobInteger(2), lobInteger(4), {Type: 203, Data: []byte("de"), LengthEncoded: true}}
		if !serveLOBCall(ctx, t, p, "CALL DBMS_LOB.WRITE(?, ?, ?, ?)", params, [][]byte{textColumn("b", 210, 63, 0)}, append([]byte{0, 0, byte(len(updated))}, updated...)) {
			return
		}
		params = []wire.Parameter{{Type: 210, Data: updated, LengthEncoded: true}, lobInteger(2)}
		if !serveLOBCall(ctx, t, p, "CALL DBMS_LOB.TRIM(?, ?)", params, [][]byte{textColumn("b", 210, 63, 0)}, append([]byte{0, 0, byte(len(trimmed))}, trimmed...)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	b, err := OpenBlob(context.Background(), pinned, "SELECT b FROM t")
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

func TestLOBTemporary(t *testing.T) {
	locator := lobInlineFixture("x")
	locator[5] |= 1
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
			return
		}
		params := []wire.Parameter{{Type: 210, Data: locator, LengthEncoded: true}, {Type: 6}}
		if !serveLOBCall(ctx, t, p, "DECLARE l BLOB := ?; BEGIN IF DBMS_LOB.ISTEMPORARY(l) = 1 THEN DBMS_LOB.FREETEMPORARY(l); END IF; ? := l; END;", params, [][]byte{textColumn("b", 210, 63, 0)}, []byte{0, 4}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	b, err := OpenBlob(context.Background(), pinned, "SELECT b FROM t")
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

func TestLOBOpenCardinalityCleanup(t *testing.T) {
	locator := lobInlineFixture("x")
	locator[5] |= 1
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator, locator) {
			return
		}
		for i := 0; i < 2; i++ {
			params := []wire.Parameter{{Type: 210, Data: locator, LengthEncoded: true}, {Type: 6}}
			if !serveLOBCall(ctx, t, p, lobFreeSQL, params, [][]byte{textColumn("b", 210, 63, 0)}, []byte{0, 4}) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	if b, err := OpenBlob(context.Background(), pinned, "SELECT b FROM t"); b != nil || !errors.Is(err, ErrLOB) {
		t.Fatal(b, err)
	}
	_ = pinned.Close()
}

func TestLOBInterleave(t *testing.T) {
	locator := lobOutFixture(3)
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT mixed") || !sendPackets(ctx, t, p,
			[]byte{1}, textColumn("a", 253, 45, 0), []byte{254, 0, 0, 6, 0}, []byte{1, 'x'}, []byte{254, 0, 0, 14, 0},
			[]byte{1}, textColumn("b", 210, 63, 0), []byte{254, 0, 0, 6, 0}, append([]byte{byte(len(locator))}, locator...), []byte{254, 0, 0, 14, 0},
			[]byte{1}, textColumn("c", 253, 45, 0), []byte{254, 0, 0, 6, 0}, []byte{1, 'z'}, []byte{254, 0, 0, 6, 0}) {
			return
		}
		params := []wire.Parameter{lobInteger(3), {Type: 210, Data: locator, LengthEncoded: true}, lobInteger(1), {Type: 6}, {Type: 6}}
		row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 3)
		row = append(row, 3, 'a', 'b', 'c')
		if !serveLOBCall(ctx, t, p, lobReadBlobSQL, params, [][]byte{textColumn("n", 8, 63, 0), textColumn("v", 203, 63, 0)}, row) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SELECT mixed")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for i, want := range []string{"x", testLOBABC, "z"} {
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

func TestLOBLargeReader(t *testing.T) {
	value := strings.Repeat("a", (16<<20)+1)
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBQuery(ctx, t, p, "SELECT b FROM t", 210, lobInlineFixture(value)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	b, err := OpenBlob(ctx, pinned, "SELECT b FROM t")
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

func TestClobOutRow(t *testing.T) {
	locator := lobOutFixture(9)
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBQuery(ctx, t, p, "SELECT c FROM t", 211, locator) {
			return
		}
		for pass := 0; pass < 2; pass++ {
			for _, part := range []struct {
				off  uint64
				text string
			}{{1, "a中"}, {3, "😀z"}} {
				params := []wire.Parameter{lobInteger(8191), {Type: 211, Data: locator, LengthEncoded: true}, lobIntFixture(part.off), {Type: 6}, {Type: 6}}
				row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 2)
				row = append(append(row, byte(len(part.text))), part.text...)
				if !serveLOBCall(ctx, t, p, lobReadClobSQL, params, [][]byte{textColumn("n", 8, 63, 0), textColumn("v", 253, 45, 0)}, row) {
					return
				}
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	c, err := OpenClob(context.Background(), pinned, "SELECT c FROM t")
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

func TestLOBBufferedTransactionContext(t *testing.T) {
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !serveLOBQueryStatus(ctx, t, p, "SELECT b FROM t", 210, 5, lobInlineFixture(testLOBABC)) {
			return
		}
		if !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(context.Background(), "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatal("buffered rows lost live transaction context", rows.Err())
	}
	var value []byte
	if err = rows.Scan(&value); err != nil || string(value) != testLOBABC {
		t.Fatal(value, err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestLOBScalarOutput(t *testing.T) {
	locator := lobOutFixture(3)
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBCall(ctx, t, p, "BEGIN ? := f(); END;", []wire.Parameter{{Type: 6}}, [][]byte{textColumn("b", 210, 63, 0)}, append([]byte{0, 0, byte(len(locator))}, locator...)) {
			return
		}
		params := []wire.Parameter{lobInteger(3), {Type: 210, Data: locator, LengthEncoded: true}, lobInteger(1), {Type: 6}, {Type: 6}}
		row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 3)
		row = append(row, 3, 'a', 'b', 'c')
		if !serveLOBCall(ctx, t, p, lobReadBlobSQL, params, [][]byte{textColumn("n", 8, 63, 0), textColumn("v", 203, 63, 0)}, row) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var value []byte
	if _, err := db.ExecContext(context.Background(), "BEGIN ? := f(); END;", Out{Dest: &value, Type: "BLOB", Length: 3}); err != nil || string(value) != testLOBABC {
		t.Fatal(value, err)
	}
}

func TestLOBOracleAmount(t *testing.T) {
	for _, typ := range []byte{3, 246} {
		t.Run(map[byte]string{3: "INTEGER", 246: "NUMBER"}[typ], func(t *testing.T) {
			locator := lobOutFixture(2)
			db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !serveLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
					return
				}
				params := []wire.Parameter{lobInteger(2), {Type: 210, Data: locator, LengthEncoded: true}, lobInteger(1), {Type: 6}, {Type: 6}}
				row := []byte{0, 0, 3, '2', 'e', '0', 2, 'a', 'b'}
				if typ == 3 {
					row = append(binary.LittleEndian.AppendUint32([]byte{0, 0}, 2), 2, 'a', 'b')
				}
				if !serveLOBCall(ctx, t, p, lobReadBlobSQL, params, [][]byte{textColumn("n", typ, 63, 0), textColumn("v", 203, 63, 0)}, row) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			b, err := OpenBlob(context.Background(), pinned, "SELECT b FROM t")
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

func TestClobUTF16OverwriteBoundary(t *testing.T) {
	for _, pair := range [][2]string{{"aX", "😀"}, {"😀X", "a"}} {
		t.Run(pair[0], func(t *testing.T) {
			db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				data, _ := charset.Encode(charsetUTF16, pair[0])
				if !readQuery(ctx, t, p, "SELECT c FROM t") || !sendPackets(ctx, t, p, []byte{1}, textColumn("c", 211, 54, 0), []byte{254, 0, 0, 6, 0}, append([]byte{byte(len(data) + 8)}, lobInlineFixture(string(data))...), []byte{254, 0, 0, 6, 0}) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			c, err := OpenClob(context.Background(), pinned, "SELECT c FROM t")
			if err != nil {
				t.Fatal(err)
			}
			if n, err := c.WriteChars(context.Background(), 0, pair[1]); n != 0 || !errors.Is(err, ErrUnsupported) {
				t.Fatal("unsafe surrogate-width overwrite", n, err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			_ = pinned.Close()
		})
	}
}

func TestLOBOutputThenRows(t *testing.T) {
	locator := lobInlineFixture(testLOBABC)
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		command, err := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 64<<20)
		if err != nil || !readCommand(ctx, t, p, command) || !sendPackets(ctx, t, p,
			combinedHeader(41, 1, 1, 4), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0},
			textColumn("out", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 6, 0},
			[]byte{0, 0, 0, 14, 0, 0, 0}, []byte{1}, textColumn("b", 210, 63, 0), []byte{254, 0, 0, 6, 0}, append([]byte{0, 0, byte(len(locator))}, locator...), []byte{254, 0, 0, 6, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 41)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var output int64
	rows, err := db.QueryContext(context.Background(), "CALL p(?)", sql.Out{Dest: &output})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	var value []byte
	if err = rows.Scan(&value); err != nil || string(value) != testLOBABC {
		t.Fatal(value, err)
	}
	if rows.Next() || rows.Err() != nil || output != 42 {
		t.Fatal(output, rows.Err())
	}
}

func TestLOBValidation(t *testing.T) {
	for _, mode := range []Mode{ModeOracle, ModeMySQL} {
		t.Run(map[Mode]string{ModeOracle: "Oracle", ModeMySQL: "MySQL"}[mode], func(t *testing.T) {
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if mode == ModeOracle && !serveLOBQuery(ctx, t, p, "SELECT b FROM t", 210, lobInlineFixture(testLOBABC)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			b, err := OpenBlob(context.Background(), pinned, "SELECT b FROM t")
			if mode == ModeMySQL {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatal(err)
				}
				_ = pinned.Close()
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = b.ReadAtContext(context.Background(), make([]byte, 2), math.MaxInt64); !errors.Is(err, ErrLOB) {
				t.Fatal(err)
			}
			if _, err = b.WriteAtContext(context.Background(), []byte("x"), -1); !errors.Is(err, ErrLOB) {
				t.Fatal(err)
			}
			if err = b.Truncate(context.Background(), 4); !errors.Is(err, ErrLOB) {
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

func TestLOBWritePartial(t *testing.T) {
	locator, updated := lobOutFixture(0), lobOutFixture(32767)
	value := []byte(strings.Repeat("a", 32768))
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
			return
		}
		params := []wire.Parameter{{Type: 210, Data: locator, LengthEncoded: true}, lobInteger(32767), lobInteger(1), {Type: 203, Data: value[:32767], LengthEncoded: true}}
		if !serveLOBCall(ctx, t, p, lobWriteSQL, params, [][]byte{textColumn("b", 210, 63, 0)}, append([]byte{0, 0, byte(len(updated))}, updated...)) {
			return
		}
		params = []wire.Parameter{{Type: 210, Data: updated, LengthEncoded: true}, lobInteger(1), lobInteger(32768), {Type: 203, Data: value[32767:], LengthEncoded: true}}
		command, err := wire.EncodePrepareExecute(0, []byte(lobWriteSQL), params, 64<<20)
		if err != nil || !readCommand(ctx, t, p, command) || !sendPackets(ctx, t, p, []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	b, err := OpenBlob(context.Background(), pinned, "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	var server *Error
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

func TestLOBChunkBudget(t *testing.T) {
	c := &conn{cfg: Config{Limits: Limits{MaxMessageBytes: 512}}}
	lob := &nativeLOBResource{raw: lobOutFixture(1000), column: wire.Column{Type: 210}}
	chunk, err := c.lobChunkSize(lob, lobWriteSQL)
	if err != nil || chunk <= 0 {
		t.Fatal(chunk, err)
	}
	params := []wire.Parameter{lob.parameter(), lobInteger(int64(chunk)), lobInteger(1), {Type: 203, Data: make([]byte, chunk), LengthEncoded: true}}
	if _, err = wire.EncodePrepareExecute(0, []byte(lobWriteSQL), params, 512); err != nil {
		t.Fatal("chunk exceeds command budget", err)
	}
	c.cfg.Limits.MaxMessageBytes = 64
	if _, err = c.lobChunkSize(lob, lobWriteSQL); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
}

func TestClobMutation(t *testing.T) {
	for _, national := range []bool{false, true} {
		t.Run(map[bool]string{false: "CLOB", true: "NCLOB"}[national], func(t *testing.T) {
			name, collation, inputType, offset, replacement, after, trimmed := charsetUTF8MB4, uint16(45), uint16(253), int64(1), "国", "a国😀z", "a国😀"
			if national {
				name, collation, inputType, offset, replacement, after, trimmed = charsetUTF16, 54, 208, 2, "😁", "a中😁z", "a中😁"
			}
			initialData, _ := charset.Encode(name, "a中😀z")
			afterData, _ := charset.Encode(name, after)
			trimmedData, _ := charset.Encode(name, trimmed)
			locator, updated, short := lobInlineFixture(string(initialData)), lobInlineFixture(string(afterData)), lobInlineFixture(string(trimmedData))
			db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !readQuery(ctx, t, p, "SELECT c FROM t") || !sendPackets(ctx, t, p, []byte{1}, textColumn("c", 211, collation, 0), []byte{254, 0, 0, 6, 0}, append([]byte{byte(len(locator))}, locator...), []byte{254, 0, 0, 6, 0}) {
					return
				}
				params := []wire.Parameter{{Type: 211, Data: locator, LengthEncoded: true}, lobInteger(lobUnits(replacement, national)), lobInteger(offset + 1), {Type: inputType, Data: []byte(replacement), LengthEncoded: true}}
				if !serveLOBCall(ctx, t, p, lobWriteSQL, params, [][]byte{textColumn("c", 211, collation, 0)}, append([]byte{0, 0, byte(len(updated))}, updated...)) {
					return
				}
				params = []wire.Parameter{{Type: 211, Data: updated, LengthEncoded: true}, lobInteger(lobUnits(trimmed, national))}
				if !serveLOBCall(ctx, t, p, lobTrimSQL, params, [][]byte{textColumn("c", 211, collation, 0)}, append([]byte{0, 0, byte(len(short))}, short...)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			c, err := OpenClob(context.Background(), pinned, "SELECT c FROM t")
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
