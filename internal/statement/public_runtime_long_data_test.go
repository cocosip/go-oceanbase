package statement_test

import (
	bytes "bytes"
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	binary "encoding/binary"
	hex "encoding/hex"
	errors "errors"
	fmt "fmt"
	io "io"
	net "net"
	strings "strings"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	charset "github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/execution"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	session "github.com/cocosip/go-oceanbase/internal/session"
	sqltext "github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	testutil "github.com/cocosip/go-oceanbase/internal/testutil"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLongDataCancelBeforeExecute(t *testing.T) {
	physical, op := rawStatementOperation(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, longDataPacket(oceanbase.ModeMySQL, 88, 0, 3, []byte("ab"))) {
			return
		}
		p.ResetSequence()
		if b, err := p.ReadMessage(ctx); err == nil {
			t.Errorf("post-upload cancellation sent %d bytes", len(b))
		}
	})
	ctx := &longQueuedContext{Context: context.Background(), done: make(chan struct{}), cancelAt: 3}
	s := statement.Adopt(op, 88, sqltext.Plan{Query: selectBindSQL, SQL: selectBindSQL, NumInput: 1, Slots: []sqltext.Slot{{Index: 0}}, Mode: config.ModeMySQL}, nil)
	op.Release()
	engine := execution.New(nil, physical)
	_, err := engine.QueryPrepared(ctx, s, []driver.NamedValue{{Ordinal: 1, Value: streamSource(&oceanbase.LongData{Reader: strings.NewReader("ab"), Length: -1})}}, execution.Options{})
	if !errors.Is(err, context.Canceled) || !physical.Closed() {
		t.Fatalf("err=%v connection valid=%t", err, !physical.Closed())
	}
}

func TestLongDataCancelAfterPiece(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	ctx := &longQueuedContext{Context: context.Background(), done: make(chan struct{})}
	raw := &longCancelConn{Conn: client, cancel: ctx.cancel}
	physical, op := statementOperation(t, outputTestConfig(), &session.Negotiated{Raw: raw, Conn: raw, Packets: wire.NewPacketIO(raw, 4096), Mode: config.ModeMySQL})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(io.Discard, server) }()
	err := statement.UploadLongData(ctx, op, 87, []any{streamSource(&oceanbase.LongData{Reader: strings.NewReader("ab"), Length: -1})})
	if !errors.Is(err, context.Canceled) || !physical.Closed() {
		t.Fatalf("err=%v connection valid=%t", err, !physical.Closed())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled upload retained server data")
	}
}

func TestLongDataIncremental(t *testing.T) {
	firstSent := make(chan struct{})
	data := bytes.Repeat([]byte{1}, 65539)
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, selectBindSQL, 86, 1, nil) || !testpeer.ReadCommand(ctx, t, p, longDataPacket(oceanbase.ModeMySQL, 86, 0, 1, data[:65536])) {
			return
		}
		close(firstSent)
		if !testpeer.ReadCommand(ctx, t, p, longDataPacket(oceanbase.ModeMySQL, 86, 0, 3, data[65536:])) || !testpeer.ServeOK(ctx, t, p, append(publictest.ExecuteHeader(86), 0, 1, 252, 0), 2) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 86)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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
	if _, err := db.ExecContext(context.Background(), selectBindSQL, &oceanbase.LongData{Reader: reader, Length: -1}); err != nil {
		t.Fatal(err)
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
			physical, op := rawStatementOperation(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadCommand(ctx, t, p, longDataPacket(oceanbase.ModeOracle, 85, 0, 3, []byte("6162"))) || !testpeer.SendPackets(ctx, t, p, tc.response) {
					return
				}
				p.ResetSequence()
				if b, err := p.ReadMessage(ctx); err == nil {
					t.Errorf("piece failure sent %d bytes", len(b))
				}
			})
			err := statement.UploadLongData(context.Background(), op, 85, []any{streamSource(&oceanbase.LongData{Reader: strings.NewReader("ab"), Length: -1})})
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			if tc.want == nil {
				var server *oceanbase.Error
				if !errors.As(err, &server) {
					t.Fatal(err)
				}
			}
			if !physical.Closed() {
				t.Fatal("failed upload retained server state")
			}
		})
	}
}

func TestLongDataPreSend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    oceanbase.Mode
		prepare oceanbase.PrepareMode
		version string
		value   *oceanbase.LongData
		want    error
	}{
		{"nil", oceanbase.ModeMySQL, oceanbase.PrepareServer, "", nil, oceanbase.ErrLongData},
		{"length", oceanbase.ModeMySQL, oceanbase.PrepareServer, "", &oceanbase.LongData{Reader: strings.NewReader(""), Length: -2}, oceanbase.ErrLongData},
		{"client", oceanbase.ModeMySQL, oceanbase.PrepareClient, "", &oceanbase.LongData{Reader: strings.NewReader(""), Length: -1}, oceanbase.ErrUnsupported},
		{"old", oceanbase.ModeOracle, oceanbase.PrepareServer, "2.2.74", &oceanbase.LongData{Reader: strings.NewReader(""), Length: -1}, oceanbase.ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close(); _ = server.Close() }()
			fault := testutil.NewFaultConn(client, testutil.Faults{WriteFailAt: 1})
			cfg := outputTestConfig()
			cfg.PrepareMode = tc.prepare
			physical, op := statementOperation(t, cfg, &session.Negotiated{Raw: fault, Conn: fault, Packets: wire.NewPacketIO(fault, 4096), Mode: tc.mode, OBVersion: tc.version})
			op.Release()
			engine := execution.New(nil, physical)
			var reads atomic.Int32
			if tc.value != nil {
				tc.value.Reader = longReaderFunc(func([]byte) (int, error) { reads.Add(1); return 0, io.EOF })
			}
			_, err := engine.Exec(context.Background(), selectBindSQL, []driver.NamedValue{{Ordinal: 1, Value: streamSource(tc.value)}}, execution.Options{})
			_, writes := fault.Counts()
			if !errors.Is(err, tc.want) || reads.Load() != 0 || writes != 0 {
				t.Fatalf("err=%v reads=%d writes=%d", err, reads.Load(), writes)
			}
		})
	}
}

func TestLongDataPartialWrite(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	fault := testutil.NewFaultConn(client, testutil.Faults{MaxWrite: 2, WriteFailAt: 2})
	physical, op := statementOperation(t, outputTestConfig(), &session.Negotiated{Raw: fault, Conn: fault, Packets: wire.NewPacketIO(fault, 4096), Mode: config.ModeMySQL})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(io.Discard, server) }()
	stream := &oceanbase.LongData{Reader: strings.NewReader("ab"), Length: -1}
	err := statement.UploadLongData(context.Background(), op, 84, []any{streamSource(stream)})
	if !errors.Is(err, testutil.ErrInjected) || !physical.Closed() {
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

func TestLongDataCancellation(t *testing.T) {
	for _, transaction := range []bool{false, true} {
		t.Run(fmt.Sprint(transaction), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := publictest.QueryDB(t, func(peerCtx context.Context, p *wire.PacketIO) {
				if transaction && !publictest.ServeTxSteps(peerCtx, t, p, []publictest.TxStep{{Query: testMySQLReadWrite, Status: 3}}) {
					return
				}
				if !publictest.ServePrepareStatus(peerCtx, t, p, selectBindSQL, 83, 1, nil, map[bool]byte{false: 2, true: 3}[transaction]) {
					return
				}
				p.ResetSequence()
				if b, err := p.ReadMessage(peerCtx); err == nil {
					t.Errorf("cancellation sent %d bytes", len(b))
				}
			})
			defer func() { _ = db.Close() }()
			var session oceanbase.Session = db
			if transaction {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				session = tx
			}
			stream := &oceanbase.LongData{Reader: longReaderFunc(func([]byte) (int, error) { cancel(); return 0, io.EOF }), Length: -1}
			_, err := session.ExecContext(ctx, selectBindSQL, stream)
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestLongDataNamedFanout(t *testing.T) {
	db := publictest.TxDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, "INSERT INTO t VALUES (?,?)", 82, 2, nil) {
			return
		}
		for _, index := range []uint16{0, 1} {
			if !testpeer.ReadCommand(ctx, t, p, longDataPacket(oceanbase.ModeOracle, 82, index, 3, []byte("6162"))) || !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
				return
			}
		}
		if !testpeer.ServeOK(ctx, t, p, append(publictest.ExecuteHeader(82), 0, 1, 254, 0, 254, 0), 2) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 82)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	stream := &oceanbase.LongData{Reader: strings.NewReader("ab"), Length: -1}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO t VALUES (:v,:V)", sql.Named("v", stream)); err != nil {
		t.Fatal(err)
	}
}

func TestLongDataSmallTextLimit(t *testing.T) {
	_, op := statementOperation(t, config.Config{Limits: config.Limits{MaxMessageBytes: 30, MaxLOBBytes: 100}}, &session.Negotiated{Mode: config.ModeOracle, OBVersion: "4.3.5"})
	if _, err := statement.LongDataParameter(op, streamSource(&oceanbase.LongData{Reader: strings.NewReader("你"), Text: true, Length: -1})); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
}

func TestLongDataText(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, name := range []string{"utf8mb4", "gb18030"} {
			t.Run(fmt.Sprintf("%d/%s", mode, name), func(t *testing.T) {
				text := strings.Repeat("a你🙂", 7000)
				want, err := charset.Encode(name, text)
				if err != nil {
					t.Fatal(err)
				}
				db := publictest.ConfiguredDB(t, config.Config{Mode: mode, Session: config.SessionOptions{Charset: name}}, func(ctx context.Context, p *wire.PacketIO) {
					if !publictest.ServePrepare(ctx, t, p, selectBindSQL, 78, 1, nil) || !serveLongContent(ctx, t, p, mode, 78, 0, true, want) {
						return
					}
					typ := byte(253)
					if mode == oceanbase.ModeOracle {
						typ = 254
					}
					if !testpeer.ServeOK(ctx, t, p, append(publictest.ExecuteHeader(78), 0, 1, typ, 0), 2) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 78)) {
						return
					}
					if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
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
				defer func() { _ = session.Close() }()
				stream := &oceanbase.LongData{Reader: strings.NewReader(text), Length: -1, Text: true}
				if _, err = session.ExecContext(context.Background(), selectBindSQL, stream); err != nil {
					t.Fatal(err)
				}
				if _, err = session.ExecContext(context.Background(), selectBindSQL, stream); !errors.Is(err, oceanbase.ErrLongData) {
					t.Fatal("consumed stream accepted", err)
				}
				if err = session.PingContext(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
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
		{"excess", strings.NewReader("ab"), 1, 0, oceanbase.ErrLongData},
		{"limit", strings.NewReader("abcdef"), -1, 3, wire.ErrMessageTooLarge},
		{"badconn", &longErrorReader{err: driver.ErrBadConn}, -1, 0, oceanbase.ErrLongData},
		{"unexpected", &longErrorReader{err: io.ErrUnexpectedEOF}, -1, 0, io.ErrUnexpectedEOF},
		{"full-error", longReaderFunc(func(p []byte) (int, error) {
			for i := range p {
				p[i] = 1
			}
			return len(p), sentinel
		}), -1, 0, sentinel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServePrepare(ctx, t, p, selectBindSQL, 77, 1, nil) {
					return
				}
				if tc.name == "reader" && !testpeer.ReadCommand(ctx, t, p, longDataPacket(oceanbase.ModeMySQL, 77, 0, 1, bytes.Repeat([]byte{1}, 65536))) {
					return
				}
				p.ResetSequence()
				if b, err := p.ReadMessage(ctx); err == nil {
					t.Errorf("failure sent another command: %d bytes", len(b))
				}
			})
			defer func() { _ = db.Close() }()
			stream := &oceanbase.LongData{Reader: tc.reader, Length: tc.length, MaxBytes: tc.max}
			_, err := db.ExecContext(context.Background(), selectBindSQL, stream)
			if !errors.Is(err, tc.want) || errors.Is(err, driver.ErrBadConn) {
				t.Fatal(err)
			}
		})
	}
}

func TestLongDataChunks(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, size := range []int{0, 3, 65536, 65539} {
			for _, known := range []bool{false, true} {
				t.Run(fmt.Sprintf("mode%d/size%d/known%t", mode, size, known), func(t *testing.T) {
					data := bytes.Repeat([]byte{0, 1, 255}, (size+2)/3)[:size]
					db := publictest.TxDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
						if !publictest.ServePrepare(ctx, t, p, "INSERT INTO t VALUES (?,?)", 77, 2, nil) {
							return
						}
						chunk := 65536
						if mode == oceanbase.ModeOracle {
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
							if mode == oceanbase.ModeOracle {
								body = []byte(hex.EncodeToString(body))
							}
							want := longDataPacket(mode, 77, 1, piece, body)
							if !testpeer.ReadCommand(ctx, t, p, want) {
								return
							}
							if mode == oceanbase.ModeOracle && !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
								return
							}
							if n < chunk {
								break
							}
						}
						typ := byte(252)
						if mode == oceanbase.ModeOracle {
							typ = 254
						}
						payload := append(publictest.ExecuteHeader(77), 0, 1, 8, 0, typ, 0)
						payload = binary.LittleEndian.AppendUint64(payload, 9)
						if !testpeer.ServeOK(ctx, t, p, payload, 2) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 77)) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					length := int64(-1)
					if known {
						length = int64(size)
					}
					reader := &longTestReader{Reader: bytes.NewReader(data)}
					stream := &oceanbase.LongData{Reader: reader, Length: length}
					if _, err := db.ExecContext(context.Background(), "INSERT INTO t VALUES (?,?)", 9, stream); err != nil {
						t.Fatal(err)
					}
					if reader.closed != 0 {
						t.Fatal("driver closed caller Reader")
					}
					if _, err := db.ExecContext(context.Background(), "INSERT INTO t VALUES (?,?)", 9, stream); !errors.Is(err, oceanbase.ErrLongData) {
						t.Fatalf("reuse consumed stream: %v", err)
					}
				})
			}
		}
	}
}
