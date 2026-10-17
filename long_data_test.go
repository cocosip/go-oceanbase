package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/testutil"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func longDataPacket(mode Mode, id uint32, index uint16, piece byte, data []byte) []byte {
	b := binary.LittleEndian.AppendUint16(stmtPacket(24, id), index)
	if mode == ModeOracle {
		b[0] = 162
		b = append(b, piece, 0)
		b = binary.LittleEndian.AppendUint64(b, uint64(len(data)))
	}
	return append(b, data...)
}

func TestLongDataChunks(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, size := range []int{0, 3, 65536, 65539} {
			for _, known := range []bool{false, true} {
				t.Run(fmt.Sprintf("mode%d/size%d/known%t", mode, size, known), func(t *testing.T) {
					data := bytes.Repeat([]byte{0, 1, 255}, (size+2)/3)[:size]
					db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
						if !servePrepare(ctx, t, p, "INSERT INTO t VALUES (?,?)", 77, 2, nil) {
							return
						}
						chunk := 65536
						if mode == ModeOracle {
							chunk = (65536 - 17) / 2
						}
						for offset := 0; ; offset += chunk {
							n := min(chunk, len(data)-offset)
							body := data[offset : offset+n]
							piece := byte(2)
							if offset == 0 {
								piece = 1
							}
							if n < chunk {
								piece = 3
							}
							if mode == ModeOracle {
								body = []byte(hex.EncodeToString(body))
							}
							want := longDataPacket(mode, 77, 1, piece, body)
							if !readCommand(ctx, t, p, want) {
								return
							}
							if mode == ModeOracle && !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
								return
							}
							if n < chunk {
								break
							}
						}
						typ := byte(252)
						if mode == ModeOracle {
							typ = 254
						}
						payload := append(executeHeader(77), 0, 1, 8, 0, typ, 0)
						payload = binary.LittleEndian.AppendUint64(payload, 9)
						if !serveOK(ctx, t, p, payload, 2) || !readCommand(ctx, t, p, stmtPacket(25, 77)) {
							return
						}
						p.ResetSequence()
						serveQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					length := int64(-1)
					if known {
						length = int64(size)
					}
					reader := &longTestReader{Reader: bytes.NewReader(data)}
					stream := &LongData{Reader: reader, Length: length}
					if _, err := db.ExecContext(context.Background(), "INSERT INTO t VALUES (?,?)", 9, stream); err != nil {
						t.Fatal(err)
					}
					if reader.closed != 0 {
						t.Fatal("driver closed caller Reader")
					}
					if !stream.used.Load() {
						t.Fatal("stream was not marked consumed")
					}
				})
			}
		}
	}
}

type longTestReader struct {
	io.Reader
	closed int
}

func (r *longTestReader) Close() error { r.closed++; return nil }

type longErrorReader struct {
	data []byte
	err  error
}

func (r *longErrorReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestLongDataFailure(t *testing.T) {
	sentinel := errors.New("reader failed")
	for _, tc := range []struct {
		name        string
		reader      io.Reader
		length, max int64
		want        error
	}{
		{"reader", &longErrorReader{data: bytes.Repeat([]byte{1}, 65536), err: sentinel}, -1, 0, sentinel},
		{"short", strings.NewReader("a"), 2, 0, io.ErrUnexpectedEOF},
		{"excess", strings.NewReader("ab"), 1, 0, ErrLongData},
		{"limit", strings.NewReader("abcdef"), -1, 3, wire.ErrMessageTooLarge},
		{"badconn", &longErrorReader{err: driver.ErrBadConn}, -1, 0, ErrLongData},
		{"unexpected", &longErrorReader{err: io.ErrUnexpectedEOF}, -1, 0, io.ErrUnexpectedEOF},
		{"full-error", longReaderFunc(func(p []byte) (int, error) {
			for i := range p {
				p[i] = 1
			}
			return len(p), sentinel
		}), -1, 0, sentinel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !servePrepare(ctx, t, p, selectBindSQL, 77, 1, nil) {
					return
				}
				if tc.name == "reader" && !readCommand(ctx, t, p, longDataPacket(ModeMySQL, 77, 0, 1, bytes.Repeat([]byte{1}, 65536))) {
					return
				}
				p.ResetSequence()
				if b, err := p.ReadMessage(ctx); err == nil {
					t.Errorf("failure sent another command: %d bytes", len(b))
				}
			})
			defer func() { _ = db.Close() }()
			stream := &LongData{Reader: tc.reader, Length: tc.length, MaxBytes: tc.max}
			_, err := db.ExecContext(context.Background(), selectBindSQL, stream)
			if !errors.Is(err, tc.want) || errors.Is(err, driver.ErrBadConn) {
				t.Fatal(err)
			}
		})
	}
}

type longReaderFunc func([]byte) (int, error)

func (f longReaderFunc) Read(p []byte) (int, error) { return f(p) }

func serveLongContent(ctx context.Context, t *testing.T, p *wire.PacketIO, mode Mode, id uint32, index uint16, text bool, want []byte) bool {
	t.Helper()
	var got []byte
	for {
		p.ResetSequence()
		b, err := p.ReadMessage(ctx)
		header := 7
		if mode == ModeOracle {
			header = 17
		}
		if err != nil || len(b) < header || binary.LittleEndian.Uint32(b[1:]) != id || binary.LittleEndian.Uint16(b[5:]) != index {
			t.Error("invalid long data packet", err)
			return false
		}
		data := b[header:]
		if mode == ModeOracle {
			if b[0] != 162 || b[8] != 0 || binary.LittleEndian.Uint64(b[9:]) != uint64(len(data)) {
				t.Error("invalid piece header")
				return false
			}
			if !text {
				data, err = hex.DecodeString(string(data))
				if err != nil {
					t.Error(err)
					return false
				}
			}
			if !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
				return false
			}
		} else if b[0] != 24 {
			t.Error("not LONG_DATA")
			return false
		}
		got = append(got, data...)
		if len(got) > len(want) {
			t.Error("too much data")
			return false
		}
		if mode == ModeOracle && b[7] == 3 || mode != ModeOracle && len(got) == len(want) {
			break
		}
	}
	if !bytes.Equal(got, want) {
		t.Error("stream content mismatch")
		return false
	}
	return true
}

func TestLongDataText(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, name := range []string{charsetUTF8MB4, "gb18030"} {
			t.Run(fmt.Sprintf("%d/%s", mode, name), func(t *testing.T) {
				text := strings.Repeat("a你🙂", 7000)
				want, err := charset.Encode(name, text)
				if err != nil {
					t.Fatal(err)
				}
				db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
					if !servePrepare(ctx, t, p, selectBindSQL, 78, 1, nil) || !serveLongContent(ctx, t, p, mode, 78, 0, true, want) {
						return
					}
					typ := byte(253)
					if mode == ModeOracle {
						typ = 254
					}
					if !serveOK(ctx, t, p, append(executeHeader(78), 0, 1, typ, 0), 2) || !readCommand(ctx, t, p, stmtPacket(25, 78)) {
						return
					}
					if !serveOK(ctx, t, p, []byte{14}, 2) {
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
				defer func() { _ = session.Close() }()
				if err = session.Raw(func(raw any) error { raw.(*conn).cfg.Session.Charset = name; return nil }); err != nil {
					t.Fatal(err)
				}
				stream := &LongData{Reader: strings.NewReader(text), Length: -1, Text: true}
				if _, err = session.ExecContext(context.Background(), selectBindSQL, stream); err != nil {
					t.Fatal(err)
				}
				if _, err = session.ExecContext(context.Background(), selectBindSQL, stream); !errors.Is(err, ErrLongData) {
					t.Fatal("consumed stream accepted", err)
				}
				if err = session.PingContext(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestLongDataOracleOutputs(t *testing.T) {
	const query = "BEGIN proc(?,?); END;"
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 79, 2, nil) || !readCommand(ctx, t, p, longDataPacket(ModeOracle, 79, 0, 3, []byte("6162"))) || !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
			return
		}
		params := []wire.Parameter{{Type: 254, LongData: true}, {Type: 6}}
		payload, err := wire.EncodePrepareExecute(79, []byte(query), params, 1<<20)
		if err != nil || !readCommand(ctx, t, p, payload) {
			t.Error(err)
			return
		}
		if !sendPackets(ctx, t, p, combinedHeader(79, 1, 2, 4), textColumn("p", 6, 63, 0), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("out", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 14, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 79)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var n int64
	if _, err := db.ExecContext(context.Background(), query, &LongData{Reader: strings.NewReader("ab"), Length: -1}, sql.Out{Dest: &n}); err != nil || n != 42 {
		t.Fatal(n, err)
	}
}

func TestLongDataCursor(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, selectBindSQL, 80, 1, nil) || !readCommand(ctx, t, p, longDataPacket(ModeMySQL, 80, 0, 3, []byte("ab"))) {
			return
		}
		payload := append(executeHeader(80), 0, 1, 252, 0)
		payload[5] = 1
		if !readCommand(ctx, t, p, payload) || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 8, 63, 0), []byte{254, 0, 0, 66, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 80)) {
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
	defer func() { _ = session.Close() }()
	cur, err := OpenCursor(context.Background(), session, selectBindSQL, CursorOptions{}, &LongData{Reader: strings.NewReader("ab"), Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLongDataMySQLOutputs(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		p.ResetSequence()
		init, err := p.ReadMessage(ctx)
		if err != nil || !strings.HasPrefix(string(init), "\x03SET @_obgo_") || !strings.HasSuffix(string(init), "=NULL") {
			t.Error("bad variable initialization", err)
			return
		}
		variable := strings.TrimSuffix(strings.TrimPrefix(string(init), "\x03SET "), "=NULL")
		if !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !servePrepare(ctx, t, p, "CALL p( ? , "+variable+" )", 81, 1, nil) {
			return
		}
		if !readCommand(ctx, t, p, longDataPacket(ModeMySQL, 81, 0, 3, []byte("ab"))) || !serveOK(ctx, t, p, append(executeHeader(81), 0, 1, 252, 0), 2) || !readCommand(ctx, t, p, stmtPacket(25, 81)) {
			return
		}
		if !readQuery(ctx, t, p, "SELECT "+variable) || !sendPackets(ctx, t, p, []byte{1}, textColumn("o", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{2, '4', '2'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SET "+variable+"=NULL"), 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var n int64
	if _, err := db.ExecContext(context.Background(), "CALL p(?,?)", &LongData{Reader: strings.NewReader("ab"), Length: -1}, sql.Out{Dest: &n}); err != nil || n != 42 {
		t.Fatal(n, err)
	}
}

func TestLongDataSmallTextLimit(t *testing.T) {
	c := &conn{cfg: Config{Limits: Limits{MaxMessageBytes: 30, MaxLOBBytes: 100}}, session: &handshakeSession{}}
	c.session.mode = ModeOracle
	c.session.obVersion = "4.3.5"
	if _, err := c.longDataParameter(&LongData{Reader: strings.NewReader("你"), Text: true, Length: -1}); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
}

func TestLongDataNamedFanout(t *testing.T) {
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "INSERT INTO t VALUES (?,?)", 82, 2, nil) {
			return
		}
		for _, index := range []uint16{0, 1} {
			if !readCommand(ctx, t, p, longDataPacket(ModeOracle, 82, index, 3, []byte("6162"))) || !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
				return
			}
		}
		if !serveOK(ctx, t, p, append(executeHeader(82), 0, 1, 254, 0, 254, 0), 2) || !readCommand(ctx, t, p, stmtPacket(25, 82)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	stream := &LongData{Reader: strings.NewReader("ab"), Length: -1}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO t VALUES (:v,:V)", sql.Named("v", stream)); err != nil {
		t.Fatal(err)
	}
}

func TestLongDataCancellation(t *testing.T) {
	for _, transaction := range []bool{false, true} {
		t.Run(fmt.Sprint(transaction), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := queryDB(t, func(peerCtx context.Context, p *wire.PacketIO) {
				if transaction && !serveTxSteps(peerCtx, t, p, []txStep{{testMySQLReadWrite, 3}}) {
					return
				}
				if !servePrepareStatus(peerCtx, t, p, selectBindSQL, 83, 1, nil, map[bool]byte{false: 2, true: 3}[transaction]) {
					return
				}
				p.ResetSequence()
				if b, err := p.ReadMessage(peerCtx); err == nil {
					t.Errorf("cancellation sent %d bytes", len(b))
				}
			})
			defer func() { _ = db.Close() }()
			var session Session = db
			if transaction {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				session = tx
			}
			stream := &LongData{Reader: longReaderFunc(func([]byte) (int, error) { cancel(); return 0, io.EOF }), Length: -1}
			_, err := session.ExecContext(ctx, selectBindSQL, stream)
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestLongDataPartialWrite(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	fault := testutil.NewFaultConn(client, testutil.Faults{MaxWrite: 2, WriteFailAt: 2})
	c := &conn{mu: make(chan struct{}, 1), cfg: outputTestConfig(), session: &handshakeSession{raw: fault, conn: fault, packets: wire.NewPacketIO(fault, 4096), mode: ModeMySQL}}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(io.Discard, server) }()
	stream := &LongData{Reader: strings.NewReader("ab"), Length: -1}
	if err := c.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := c.uploadLongDataLocked(context.Background(), 84, []boundValue{{value: stream}})
	c.unlock()
	if !errors.Is(err, testutil.ErrInjected) || c.IsValid() {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("partial write left transport open")
	}
	_, writes := fault.Counts()
	if writes != 2 {
		t.Fatal("upload retried", writes)
	}
}

func TestLongDataPreSend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    Mode
		prepare PrepareMode
		version string
		value   *LongData
		want    error
	}{
		{"nil", ModeMySQL, PrepareServer, "", nil, ErrLongData},
		{"length", ModeMySQL, PrepareServer, "", &LongData{Reader: strings.NewReader(""), Length: -2}, ErrLongData},
		{"client", ModeMySQL, PrepareClient, "", &LongData{Reader: strings.NewReader(""), Length: -1}, ErrUnsupported},
		{"old", ModeOracle, PrepareServer, "2.2.74", &LongData{Reader: strings.NewReader(""), Length: -1}, ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close(); _ = server.Close() }()
			fault := testutil.NewFaultConn(client, testutil.Faults{WriteFailAt: 1})
			c := &conn{mu: make(chan struct{}, 1), cfg: outputTestConfig(), session: &handshakeSession{raw: fault, conn: fault, packets: wire.NewPacketIO(fault, 4096), mode: tc.mode, obVersion: tc.version}}
			c.cfg.PrepareMode = tc.prepare
			var reads atomic.Int32
			if tc.value != nil {
				tc.value.Reader = longReaderFunc(func([]byte) (int, error) { reads.Add(1); return 0, io.EOF })
			}
			_, err := c.ExecContext(context.Background(), selectBindSQL, []driver.NamedValue{{Ordinal: 1, Value: tc.value}})
			_, writes := fault.Counts()
			if !errors.Is(err, tc.want) || reads.Load() != 0 || writes != 0 {
				t.Fatalf("err=%v reads=%d writes=%d", err, reads.Load(), writes)
			}
		})
	}
}

func TestLongDataPieceFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response []byte
		want     error
	}{
		{"server", []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}, nil},
		{"malformed", []byte{0}, wire.ErrMalformed},
		{"more", []byte{0, 0, 0, 10, 0, 0, 0}, wire.ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				if !readCommand(ctx, t, p, longDataPacket(ModeOracle, 85, 0, 3, []byte("6162"))) || !sendPackets(ctx, t, p, tc.response) {
					return
				}
				p.ResetSequence()
				if b, err := p.ReadMessage(ctx); err == nil {
					t.Errorf("piece failure sent %d bytes", len(b))
				}
			})
			if err := c.lock(context.Background()); err != nil {
				t.Fatal(err)
			}
			err := c.uploadLongDataLocked(context.Background(), 85, []boundValue{{value: &LongData{Reader: strings.NewReader("ab"), Length: -1}}})
			c.unlock()
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			if tc.want == nil {
				var server *Error
				if !errors.As(err, &server) {
					t.Fatal(err)
				}
			}
			if c.IsValid() {
				t.Fatal("failed upload retained server state")
			}
		})
	}
}

func TestLongDataIncremental(t *testing.T) {
	firstSent := make(chan struct{})
	data := bytes.Repeat([]byte{1}, 65539)
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, selectBindSQL, 86, 1, nil) || !readCommand(ctx, t, p, longDataPacket(ModeMySQL, 86, 0, 1, data[:65536])) {
			return
		}
		close(firstSent)
		if !readCommand(ctx, t, p, longDataPacket(ModeMySQL, 86, 0, 3, data[65536:])) || !serveOK(ctx, t, p, append(executeHeader(86), 0, 1, 252, 0), 2) || !readCommand(ctx, t, p, stmtPacket(25, 86)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	offset := 0
	reader := longReaderFunc(func(p []byte) (int, error) {
		if offset > 0 {
			select {
			case <-firstSent:
			case <-time.After(time.Second):
				return 0, errors.New("content buffered before first upload")
			}
		}
		if offset == len(data) {
			return 0, io.EOF
		}
		n := copy(p, data[offset:])
		offset += n
		return n, nil
	})
	if _, err := db.ExecContext(context.Background(), selectBindSQL, &LongData{Reader: reader, Length: -1}); err != nil {
		t.Fatal(err)
	}
}

func TestLongDataReaderBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader io.Reader
		want   error
	}{
		{"idle", longReaderFunc(func([]byte) (int, error) { return 0, nil }), io.ErrNoProgress},
		{"negative", longReaderFunc(func([]byte) (int, error) { return -1, nil }), ErrLongData},
		{"large", longReaderFunc(func(p []byte) (int, error) { return len(p) + 1, nil }), ErrLongData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &longSource{ctx: context.Background(), value: &LongData{Reader: tc.reader, Length: -1}, limit: 100}
			if _, err := readLongChunk(s, make([]byte, 4)); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestLongDataNilReader(t *testing.T) {
	for _, reader := range []io.Reader{nil, (*bytes.Reader)(nil), longReaderFunc(nil), longMapReader(nil), longSliceReader(nil), longChanReader(nil)} {
		t.Run(fmt.Sprintf("%T", reader), func(t *testing.T) {
			if _, err := convertArgument(&LongData{Reader: reader, Length: -1}); !errors.Is(err, ErrLongData) {
				t.Fatalf("nil Reader accepted: %v", err)
			}
		})
	}
}

type longMapReader map[string]byte

func (longMapReader) Read([]byte) (int, error) { return 0, io.EOF }

type longSliceReader []byte

func (longSliceReader) Read([]byte) (int, error) { return 0, io.EOF }

type longChanReader chan byte

func (longChanReader) Read([]byte) (int, error) { return 0, io.EOF }

func TestLongDataNilReaderSession(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				readers := []io.Reader{longReaderFunc(func([]byte) (int, error) { return 0, io.EOF }), longMapReader{}, longSliceReader{}, make(longChanReader)}
				status := byte(2)
				typ := byte(252)
				if mode == ModeOracle {
					status, typ = 6, 254
				}
				db := batchRewriteDB(t, Config{Mode: mode}, framing, func(ctx context.Context, p *wire.PacketIO) {
					for range readers {
						if !servePrepareStatus(ctx, t, p, selectBindSQL, 77, 1, nil, status) ||
							!readCommand(ctx, t, p, longDataPacket(mode, 77, 0, 3, nil)) {
							return
						}
						if mode == ModeOracle && !sendPackets(ctx, t, p, []byte{0, 0, 0, status, 0, 0, 0}) {
							return
						}
						if !serveOK(ctx, t, p, append(executeHeader(77), 0, 1, typ, 0), status) || !readCommand(ctx, t, p, stmtPacket(25, 77)) {
							return
						}
					}
					if !serveOK(ctx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				input := &LongData{Reader: longReaderFunc(nil), Length: -1}
				if _, err := db.ExecContext(context.Background(), selectBindSQL, input); !errors.Is(err, ErrLongData) {
					t.Fatal("nil Reader was not rejected before prepare/upload", err)
				}
				if input.used.Load() {
					t.Fatal("rejected Reader consumed the input")
				}
				for i, reader := range readers {
					if i != 0 {
						input = &LongData{}
					}
					input.Reader = reader
					if _, err := db.ExecContext(context.Background(), selectBindSQL, input); err != nil {
						t.Fatalf("non-nil empty %T rejected: %v", reader, err)
					}
					if !input.used.Load() {
						t.Fatal("valid Reader was not consumed")
					}
				}
				if err := db.Ping(); err != nil {
					t.Fatal("Reader validation disturbed session reuse", err)
				}
			})
		}
	}
}

// Model cancellation whose queued callbacks are stopped before the scheduler runs them.
type longQueuedContext struct {
	context.Context
	done          chan struct{}
	canceled      atomic.Bool
	registrations atomic.Int32
	cancelAt      int32
}

func (c *longQueuedContext) Done() <-chan struct{} { return c.done }
func (c *longQueuedContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}
func (c *longQueuedContext) AfterFunc(func()) func() bool {
	if c.registrations.Add(1) == c.cancelAt {
		c.cancel()
	}
	return func() bool { return true }
}
func (c *longQueuedContext) cancel() {
	if c.canceled.CompareAndSwap(false, true) {
		close(c.done)
	}
}

type longCancelConn struct {
	net.Conn
	written int
	cancel  func()
}

func (c *longCancelConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written += n
	if c.written >= 13 {
		c.cancel()
	}
	return n, err
}

func TestLongDataCancelAfterPiece(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	ctx := &longQueuedContext{Context: context.Background(), done: make(chan struct{})}
	raw := &longCancelConn{Conn: client, cancel: ctx.cancel}
	c := &conn{mu: make(chan struct{}, 1), cfg: outputTestConfig(), session: &handshakeSession{raw: raw, conn: raw, packets: wire.NewPacketIO(raw, 4096), mode: ModeMySQL}}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(io.Discard, server) }()
	if err := c.lock(ctx); err != nil {
		t.Fatal(err)
	}
	err := c.uploadLongDataLocked(ctx, 87, []boundValue{{value: &LongData{Reader: strings.NewReader("ab"), Length: -1}}})
	c.unlock()
	if !errors.Is(err, context.Canceled) || c.IsValid() {
		t.Fatalf("err=%v connection valid=%t", err, c.IsValid())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled upload retained server data")
	}
}

func TestLongDataCancelBeforeExecute(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !readCommand(ctx, t, p, longDataPacket(ModeMySQL, 88, 0, 3, []byte("ab"))) {
			return
		}
		p.ResetSequence()
		if b, err := p.ReadMessage(ctx); err == nil {
			t.Errorf("post-upload cancellation sent %d bytes", len(b))
		}
	})
	c.session.mode = ModeMySQL
	ctx := &longQueuedContext{Context: context.Background(), done: make(chan struct{}), cancelAt: 3}
	s := &stmt{conn: c, id: 88, plan: bindPlan{numInput: 1, slots: []bindSlot{{index: 0}}, mode: ModeMySQL}}
	_, err := s.ExecContext(ctx, []driver.NamedValue{{Ordinal: 1, Value: &LongData{Reader: strings.NewReader("ab"), Length: -1}}})
	if !errors.Is(err, context.Canceled) || c.IsValid() {
		t.Fatalf("err=%v connection valid=%t", err, c.IsValid())
	}
}

func FuzzLongTextBoundary(f *testing.F) {
	for _, seed := range [][]byte{[]byte("a你🙂"), {0xe4, 0xbd}, {0xff}, {0xe4, 0x41}, {}} {
		f.Add(seed, true)
		f.Add(seed, false)
	}
	f.Fuzz(func(t *testing.T, data []byte, final bool) {
		if len(data) > 65536 {
			return
		}
		end, err := longTextBoundary(data, final)
		valid := utf8.Valid(data)
		partial := -1
		if !final && !valid {
			for n := 1; n <= 3 && n <= len(data); n++ {
				i := len(data) - n
				if utf8.Valid(data[:i]) && !utf8.FullRune(data[i:]) {
					partial = i
					break
				}
			}
		}
		if valid {
			if err != nil || end != len(data) {
				t.Fatal("valid text rejected")
			}
			return
		}
		if partial >= 0 {
			if err != nil || end != partial {
				t.Fatal("incomplete rune mishandled")
			}
			return
		}
		if !errors.Is(err, charset.ErrEncoding) {
			t.Fatal("invalid text accepted")
		}
	})
}
