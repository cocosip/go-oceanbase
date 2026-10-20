package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const localImportSQL = "LOAD DATA LOCAL INFILE 'input' INTO TABLE t"
const localInputName = "input"
const localSuccessStage = "source-success"
const localEmptyStage = "empty-source"
const localNilStage = "nil-source"
const localReaderStage = "source-reader"
const localErrorStage = "server-error"
const localFilenameCharset = "gbk"
const localTypedNilStage = "typed-nil-source"
const localMalformedNameStage = "malformed-infile-name"

func TestMySQLImportStatementSyntax(t *testing.T) {
	for _, tc := range []struct {
		query              string
		noBackslash, valid bool
	}{
		{localImportSQL, false, true},
		{"/* import */ " + localImportSQL + " ; -- done\n", false, true},
		{localImportSQL + " FIELDS TERMINATED BY ';'", false, true},
		{`LOAD DATA LOCAL INFILE 'input' INTO TABLE ` + "`t;name`", false, true},
		{localImportSQL + ` FIELDS TERMINATED BY '\\'`, false, true},
		{localImportSQL + ` FIELDS TERMINATED BY '\'`, true, true},
		{localImportSQL + ";;", false, false},
		{localImportSQL + "; SET autocommit=0", false, false},
		{`LOAD DATA LOCAL INFILE 'input' INTO TABLE "t\"; START TRANSACTION; -- "`, false, false},
		{localImportSQL + " /* unterminated", false, false},
		{"SELECT 'LOAD DATA'", false, false},
	} {
		err := localImportStatement(tc.query, ModeMySQL, tc.noBackslash)
		if (err == nil) != tc.valid || err != nil && !errors.Is(err, ErrUnsupported) {
			t.Errorf("query=%q noBackslash=%v: %v", tc.query, tc.noBackslash, err)
		}
	}
}

func TestLocalInfileMySQLCommandBoundary(t *testing.T) {
	for _, query := range []string{
		"START TRANSACTION; " + localImportSQL,
		localImportSQL + "; START TRANSACTION",
		"CALL unsafe_import()",
		"/*! START TRANSACTION; */ " + localImportSQL,
	} {
		t.Run(query, func(t *testing.T) {
			db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
				if !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			reader := strings.NewReader("x")
			_, err := Import(context.Background(), db, query, &LocalInfile{Name: localInputName, Reader: reader})
			if !errors.Is(err, ErrUnsupported) || !errors.Is(err, ErrLocalInfile) || reader.Len() != 1 {
				t.Fatal("unsafe command sent or input consumed", err, reader.Len())
			}
			if err = db.PingContext(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalInfileOracleCommandBoundary(t *testing.T) {
	for _, query := range []string{
		"SET TRANSACTION READ WRITE; " + localImportSQL,
		localImportSQL + "; SET TRANSACTION READ WRITE",
		"BEGIN unsafe_import; END;",
	} {
		t.Run(query, func(t *testing.T) {
			db := localInfileDB(t, ModeOracle, true, func(ctx context.Context, p *wire.PacketIO) {
				if !serveOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			reader := strings.NewReader("x")
			_, err := Import(context.Background(), db, query, &LocalInfile{Name: localInputName, Reader: reader})
			if !errors.Is(err, ErrUnsupported) || reader.Len() != 1 {
				t.Fatal("unsafe Oracle command sent or input consumed", err, reader.Len())
			}
			if err = db.PingContext(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalInfileOracleManualTransactionRefused(t *testing.T) {
	for _, state := range []struct {
		command string
		status  byte
	}{{testAutocommitOff, 4}, {testOracleReadWrite, 7}} {
		t.Run(state.command, func(t *testing.T) {
			db := localInfileDB(t, ModeOracle, true, func(ctx context.Context, p *wire.PacketIO) {
				if !serveOK(ctx, t, p, []byte("\x03"+state.command), state.status) || !serveOK(ctx, t, p, []byte("\x03ROLLBACK"), 6) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			c, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			if _, err = c.ExecContext(context.Background(), state.command); err != nil {
				t.Fatal(err)
			}
			reader := strings.NewReader("x")
			if _, err = Import(context.Background(), c, localImportSQL, &LocalInfile{Name: localInputName, Reader: reader}); !errors.Is(err, ErrUnsupported) || reader.Len() != 1 {
				t.Fatal("manual Oracle transaction allowed import", err, reader.Len())
			}
			if _, err = c.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func localInfileDB(t *testing.T, mode Mode, capable bool, serve func(context.Context, *wire.PacketIO), configure ...func(*Config)) *sql.DB {
	t.Helper()
	cfg := Config{User: testUser, Mode: mode}
	cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		greeting := authGreeting(authNative, false)
		compressed := cfg.Protocol.Compression == FeatureRequired
		ob20 := cfg.Protocol.OB20 == FeatureRequired
		if compressed || ob20 {
			greeting = protocolGreeting(compressed)
		}
		if capable {
			offset := bytes.IndexByte(greeting[1:], 0) + 1 + 1 + 4 + 8 + 1
			greeting[offset] |= 128
		}
		if !sendPackets(ctx, t, p, greeting) {
			return
		}
		response, err := p.ReadMessage(ctx)
		if err != nil || len(response) < 4 || (binary.LittleEndian.Uint32(response)&128 != 0) != capable {
			t.Error("local infile capability mismatch", err)
			return
		}
		status := byte(2)
		if mode == ModeOracle {
			status = 6
		}
		var variables map[string]string
		if ob20 {
			variables = map[string]string{testProxyCapability: "256"}
		}
		if !sendPackets(ctx, t, p, protocolOK(status, variables)) {
			return
		}
		if compressed {
			if err := p.EnableCompression(); err != nil {
				t.Error(err)
				return
			}
		}
		if ob20 {
			if err := p.EnableOB20(wire.OB20Options{ConnectionID: 42}); err != nil {
				t.Error(err)
				return
			}
		}
		if !serveBaseline(ctx, t, p, status) {
			return
		}
		serve(context.Background(), p)
	})
	for _, f := range configure {
		f(&cfg)
	}
	c, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

func localRequest(ctx context.Context, t *testing.T, p *wire.PacketIO, name string) bool {
	t.Helper()
	return readQuery(ctx, t, p, localImportSQL) && sendPackets(ctx, t, p, append([]byte{251}, name...))
}

func readLocalUpload(ctx context.Context, t *testing.T, p *wire.PacketIO) []byte {
	t.Helper()
	var result []byte
	for {
		b, err := p.ReadMessage(ctx)
		if err != nil {
			t.Error(err)
			return nil
		}
		if len(b) == 0 {
			return result
		}
		result = append(result, b...)
	}
}

func TestLocalInfilePolicy(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: "mysql-mode", ModeOracle: "oracle-mode"}[mode], func(t *testing.T) {
			payload := bytes.Repeat([]byte{0, 1, 255, 10}, 20000)
			db := localInfileDB(t, mode, true, func(ctx context.Context, p *wire.PacketIO) {
				if !localRequest(ctx, t, p, localInputName) {
					return
				}
				if !bytes.Equal(readLocalUpload(ctx, t, p), payload) {
					t.Error("upload was altered")
				}
				if !sendPackets(ctx, t, p, []byte{0, 5, 0, 2, 0, 0, 0}) || !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			reader := &longTestReader{Reader: bytes.NewReader(payload)}
			input := &LocalInfile{Name: localInputName, Reader: reader}
			result, err := Import(context.Background(), db, localImportSQL, input)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := result.RowsAffected(); err != nil || n != 5 || reader.closed != 0 {
				t.Fatal("result or Reader ownership", n, reader.closed, err)
			}
			if _, err = Import(context.Background(), db, localImportSQL, input); !errors.Is(err, ErrLocalInfile) {
				t.Fatal("input reused", err)
			}
			if err = db.Ping(); err != nil {
				t.Fatal("upload left unread response", err)
			}
		})
	}
}

func TestLocalInfileFailure(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsolicited", true: "wrong-name"}[authorized], func(t *testing.T) {
			var reads atomic.Int32
			db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
				if !localRequest(ctx, t, p, "sensitive-file") {
					return
				}
				if b, err := p.ReadMessage(ctx); err == nil || len(b) != 0 {
					t.Error("unauthorized response sent", b, err)
				}
			})
			defer func() { _ = db.Close() }()
			var err error
			if authorized {
				_, err = Import(context.Background(), db, localImportSQL, &LocalInfile{Name: localInputName, Reader: longReaderFunc(func([]byte) (int, error) { reads.Add(1); return 0, io.EOF })})
			} else {
				_, err = db.ExecContext(context.Background(), localImportSQL)
			}
			if !errors.Is(err, ErrLocalInfile) || reads.Load() != 0 {
				t.Fatal("unauthorized source accessed", reads.Load(), err)
			}
		})
	}
}

func TestLocalInfileSourceFailure(t *testing.T) {
	sentinel := errors.New("infile source failed")
	for _, tc := range []struct {
		name   string
		reader io.Reader
		max    int64
		want   error
	}{
		{localReaderStage, &longErrorReader{data: bytes.Repeat([]byte{1}, 65536), err: sentinel}, 0, sentinel},
		{"limit", bytes.NewReader([]byte("abcdef")), 3, wire.ErrMessageTooLarge},
		{"exact-limit", &longErrorReader{data: []byte(testLOBABC), err: sentinel}, 3, sentinel},
		{"retry", &longErrorReader{err: driver.ErrBadConn}, 0, ErrLocalInfile},
		{"idle", longReaderFunc(func([]byte) (int, error) { return 0, nil }), 0, io.ErrNoProgress},
		{"invalid-count", longReaderFunc(func(b []byte) (int, error) { return len(b) + 1, nil }), 0, ErrLongData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dialed atomic.Int32
			db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
				if !localRequest(ctx, t, p, localInputName) {
					return
				}
				for {
					b, err := p.ReadMessage(ctx)
					if err != nil {
						return
					}
					if len(b) == 0 {
						t.Error("failed source sent successful EOF")
						return
					}
				}
			}, func(cfg *Config) {
				dial := cfg.DialContext
				cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					dialed.Add(1)
					return dial(ctx, network, addr)
				}
			})
			defer func() { _ = db.Close() }()
			_, err := Import(context.Background(), db, localImportSQL, &LocalInfile{Name: localInputName, Reader: tc.reader, MaxBytes: tc.max})
			if !errors.Is(err, tc.want) || errors.Is(err, driver.ErrBadConn) || dialed.Load() != 1 {
				t.Fatal("source error/replay", dialed.Load(), err)
			}
		})
	}
}

type localOwnedReader struct {
	io.Reader
	closed   int
	closeErr error
}

func (r *localOwnedReader) Close() error { r.closed++; return r.closeErr }

func TestLocalInfileResolver(t *testing.T) {
	sentinel := errors.New("resolver failed")
	for _, stage := range []string{localSuccessStage, localEmptyStage, "open-error", "close-error", localNilStage, localTypedNilStage} {
		t.Run(stage, func(t *testing.T) {
			reader := &localOwnedReader{Reader: bytes.NewBufferString(testLOBABC)}
			if stage == localEmptyStage {
				reader.Reader = bytes.NewReader(nil)
			}
			if stage == "close-error" {
				reader.closeErr = sentinel
			}
			ok := stage == localSuccessStage || stage == localEmptyStage
			db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
				if !localRequest(ctx, t, p, localInputName) {
					return
				}
				if !ok {
					if b, err := p.ReadMessage(ctx); err == nil && len(b) == 0 {
						t.Error("resolver error completed upload")
					}
					return
				}
				payload := readLocalUpload(ctx, t, p)
				if (stage == localEmptyStage && len(payload) != 0) || (stage != localEmptyStage && string(payload) != testLOBABC) {
					t.Error("resolver content", payload)
				}
				if !sendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 0, 0}) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			calls := 0
			_, err := Import(context.Background(), db, localImportSQL, &LocalInfile{Open: func(ctx context.Context, name string) (io.ReadCloser, error) {
				calls++
				if ctx.Err() != nil || name != localInputName {
					t.Error("resolver context/name", name, ctx.Err())
				}
				switch stage {
				case "open-error":
					return reader, sentinel
				case localNilStage:
					return nil, nil
				case localTypedNilStage:
					return (*localOwnedReader)(nil), nil
				default:
					return reader, nil
				}
			}})
			wantClosed := 1
			if stage == localNilStage || stage == localTypedNilStage {
				wantClosed = 0
			}
			if (ok && err != nil) || (!ok && !errors.Is(err, ErrLocalInfile)) || calls != 1 || reader.closed != wantClosed {
				t.Fatal("resolver lifecycle", calls, reader.closed, err)
			}
		})
	}
}

func TestLocalInfileTimeout(t *testing.T) {
	for _, stage := range []string{localReaderStage, "ack"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := localInfileDB(t, ModeMySQL, true, func(peerCtx context.Context, p *wire.PacketIO) {
				if !localRequest(peerCtx, t, p, localInputName) {
					return
				}
				if stage == "ack" {
					_ = readLocalUpload(peerCtx, t, p)
				}
				if b, err := p.ReadMessage(peerCtx); err == nil {
					t.Error("canceled upload retained transport", b)
				}
			})
			defer func() { _ = db.Close() }()
			input := &LocalInfile{Name: localInputName, Reader: bytes.NewBufferString(testLOBABC), Timeout: 200 * time.Millisecond}
			want := context.DeadlineExceeded
			if stage == localReaderStage {
				want = context.Canceled
				input.Reader = longReaderFunc(func([]byte) (int, error) { cancel(); return 0, io.EOF })
			}
			if _, err := Import(ctx, db, localImportSQL, input); !errors.Is(err, want) {
				t.Fatal("upload cancellation", err)
			}
		})
	}
}

func TestLocalInfileValidation(t *testing.T) {
	for _, input := range []*LocalInfile{
		nil, {}, {Name: localInputName}, {Reader: strings.NewReader("x")},
		{Name: "a\x00b", Reader: strings.NewReader("x")}, {Name: "\xff", Reader: strings.NewReader("x")},
		{Name: strings.Repeat("x", 4097), Reader: strings.NewReader("x")},
		{Name: localInputName, Reader: (*bytes.Reader)(nil)},
		{Name: localInputName, Reader: strings.NewReader("x"), MaxBytes: -1},
		{Name: localInputName, Reader: strings.NewReader("x"), Timeout: -1},
		{Name: localInputName, Reader: strings.NewReader("x"), Open: func(context.Context, string) (io.ReadCloser, error) {
			t.Fatal("invalid source opened")
			return nil, nil
		}},
	} {
		if _, err := Import(context.Background(), nil, localImportSQL, input); !errors.Is(err, ErrLocalInfile) {
			t.Fatal("invalid input accepted", err)
		}
	}
	db := localInfileDB(t, ModeMySQL, false, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); serveQuit(t, p) })
	defer func() { _ = db.Close() }()
	if _, err := Import(context.Background(), db, localImportSQL, &LocalInfile{Name: localInputName, Reader: strings.NewReader("x")}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unnegotiated capability", err)
	}
}

func TestLocalInfileServerResponse(t *testing.T) {
	for _, stage := range []string{localErrorStage, "repeat", localMalformedNameStage, "malformed-final"} {
		t.Run(stage, func(t *testing.T) {
			var opened atomic.Int32
			db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
				name := localInputName
				if stage == localMalformedNameStage {
					name = "invalid\x00infile"
				}
				if !localRequest(ctx, t, p, name) {
					return
				}
				if stage != localMalformedNameStage {
					_ = readLocalUpload(ctx, t, p)
					switch stage {
					case localErrorStage:
						if !sendPackets(ctx, t, p, []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}) || !serveOK(ctx, t, p, []byte{14}, 2) {
							return
						}
						p.ResetSequence()
						serveQuit(t, p)
						return
					case "repeat":
						if !sendPackets(ctx, t, p, append([]byte{251}, localInputName...)) {
							return
						}
					case "malformed-final":
						if !sendPackets(ctx, t, p, []byte{0}) {
							return
						}
					}
				}
				if b, err := p.ReadMessage(ctx); err == nil {
					t.Error("invalid import retained stream", b)
				}
			})
			defer func() { _ = db.Close() }()
			_, err := Import(context.Background(), db, localImportSQL, &LocalInfile{Open: func(context.Context, string) (io.ReadCloser, error) {
				opened.Add(1)
				return io.NopCloser(strings.NewReader("x")), nil
			}})
			var server *Error
			if stage == localErrorStage {
				if !errors.As(err, &server) || server.Code != 1 {
					t.Fatal("lost server error", err)
				}
				if err = db.Ping(); err != nil {
					t.Fatal("complete server rejection poisoned session", err)
				}
			} else if err == nil {
				t.Fatal("invalid request accepted")
			}
			want := int32(1)
			if stage == localMalformedNameStage {
				want = 0
			}
			if opened.Load() != want {
				t.Fatal("resolver invoked for invalid or repeated request", opened.Load())
			}
		})
	}
}

func TestLocalInfileOracleTransactionRefused(t *testing.T) {
	db := localInfileDB(t, ModeOracle, true, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}, {testRollbackSQL, 4}, {testAutocommitOn, 6}}) || !serveOK(ctx, t, p, []byte{14}, 6) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	reader := strings.NewReader("1\tx\n")
	if _, err = Import(context.Background(), tx, localImportSQL, &LocalInfile{Name: localInputName, Reader: reader}); !errors.Is(err, ErrUnsupported) || reader.Len() != 4 {
		t.Fatal("Oracle transactional import sent SQL or consumed input", err, reader.Len())
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal("refused Oracle import damaged the session", err)
	}
}

func TestLocalInfileMySQLTransactionRefused(t *testing.T) {
	var reads atomic.Int32
	db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}, {testRollbackSQL, 2}}) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = Import(context.Background(), tx, localImportSQL, &LocalInfile{Name: localInputName, Reader: longReaderFunc(func([]byte) (int, error) { reads.Add(1); return 0, io.EOF })})
	if !errors.Is(err, ErrUnsupported) || !errors.Is(err, ErrLocalInfile) || reads.Load() != 0 {
		t.Fatalf("unsafe transaction import: reads=%d err=%v", reads.Load(), err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalInfileMySQLManualTransactionRefused(t *testing.T) {
	for _, command := range []string{"SET autocommit=0", testManualTxStart} {
		t.Run(command, func(t *testing.T) {
			status := byte(0)
			if command == testManualTxStart {
				status = 3
			}
			db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
				if !serveOK(ctx, t, p, []byte("\x03"+command), status) || !serveOK(ctx, t, p, []byte("\x03ROLLBACK"), 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			c, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			if _, err := c.ExecContext(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			if _, err := Import(context.Background(), c, localImportSQL, &LocalInfile{Name: localInputName, Reader: strings.NewReader("x")}); !errors.Is(err, ErrUnsupported) {
				t.Fatal(err)
			}
			if _, err := c.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalInfileMySQLXASessionRefused(t *testing.T) {
	for _, ended := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "ended"}[ended], func(t *testing.T) {
			db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
				if !serveOK(ctx, t, p, []byte("\x03"+testXAStartSQL), 2) {
					return
				}
				if !serveOK(ctx, t, p, []byte("\x03"+testXAEndSQL), 2) || !serveOK(ctx, t, p, []byte("\x03XA ROLLBACK X'67',X'',0x1"), 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			c, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			x := XID{FormatID: 1, GlobalID: []byte("g")}
			if err := XAStart(context.Background(), c, x, XANoFlags); err != nil {
				t.Fatal(err)
			}
			if ended {
				if err := XAEnd(context.Background(), c, x, XASuccess); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Import(context.Background(), c, localImportSQL, &LocalInfile{Name: localInputName, Reader: strings.NewReader("x")}); !errors.Is(err, ErrUnsupported) {
				t.Fatal(err)
			}
			if !ended {
				if err := XAEnd(context.Background(), c, x, XASuccess); err != nil {
					t.Fatal(err)
				}
			}
			if err := XARollback(context.Background(), c, x); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalInfileSequenceWrap(t *testing.T) {
	payload := bytes.Repeat([]byte{3, 's'}, 8<<20)
	db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
		if !localRequest(ctx, t, p, localInputName) {
			return
		}
		if !bytes.Equal(readLocalUpload(ctx, t, p), payload) {
			t.Error("large import corruption")
		}
		if !sendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if _, err := Import(context.Background(), db, localImportSQL, &LocalInfile{Name: localInputName, Reader: bytes.NewReader(payload), MaxBytes: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalInfileFinalResultsetRejected(t *testing.T) {
	db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
		if !localRequest(ctx, t, p, localInputName) {
			return
		}
		_ = readLocalUpload(ctx, t, p)
		// A plausible rowset is still not an import acknowledgment.
		if !sendPackets(ctx, t, p, []byte{1}) {
			return
		}
		for _, data := range [][]byte{textColumn("v", 253, 45, 0), {254, 0, 0, 2, 0}, {1, 'x'}, {254, 0, 0, 2, 0}} {
			if err := p.WriteMessage(ctx, data); err != nil {
				return
			}
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	if _, err := Import(context.Background(), db, localImportSQL, &LocalInfile{Name: localInputName, Reader: strings.NewReader("x")}); !errors.Is(err, wire.ErrMalformed) {
		t.Fatal("non-OK/ERR import acknowledgment accepted", err)
	}
}

func TestLocalInfileFilenameCharset(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan []byte, 1)
	go func() { done <- readLocalUpload(ctx, t, wire.NewPacketIO(server, 1024)) }()
	c := &conn{cfg: Config{Session: SessionOptions{Charset: localFilenameCharset}, Limits: Limits{MaxMessageBytes: 1024, MaxLOBBytes: 1024}}, session: &handshakeSession{capabilities: wire.ClientLocalFiles, packets: wire.NewPacketIO(client, 1024)}}
	r := &textRows{conn: c, localInfile: &LocalInfile{Name: "输入", Reader: strings.NewReader("data")}}
	err := r.uploadLocalInfile(ctx, []byte{251, 0xca, 0xe4, 0xc8, 0xeb})
	if err != nil {
		_ = client.Close()
	}
	got := <-done
	if err != nil || string(got) != "data" {
		t.Fatal("session charset filename mismatch", got, err)
	}
}

func TestLocalInfileFraming(t *testing.T) {
	for _, framing := range []string{testCompression, testOB20} {
		t.Run(framing, func(t *testing.T) {
			payload := bytes.Repeat([]byte{0, 1, 255, 3}, 40000)
			db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
				if !p.Framed() {
					t.Error("unframed import")
					return
				}
				if !localRequest(ctx, t, p, localInputName) {
					return
				}
				if !bytes.Equal(readLocalUpload(ctx, t, p), payload) {
					t.Error("framed upload corruption")
				}
				if !sendPackets(ctx, t, p, []byte{0, 2, 0, 2, 0, 0, 0}) || !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			}, func(cfg *Config) {
				if framing == testCompression {
					cfg.Protocol.Compression = FeatureRequired
				} else {
					cfg.Protocol.OB20 = FeatureRequired
				}
			})
			defer func() { _ = db.Close() }()
			if _, err := Import(context.Background(), db, localImportSQL, &LocalInfile{Name: localInputName, Reader: bytes.NewReader(payload)}); err != nil {
				t.Fatal(err)
			}
			if err := db.Ping(); err != nil {
				t.Fatal("framed response not fully consumed", err)
			}
		})
	}
}

const testManualTxStart = "START TRANSACTION"
