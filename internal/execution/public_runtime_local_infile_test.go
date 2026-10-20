package execution_test

import (
	bytes "bytes"
	context "context"
	driver "database/sql/driver"
	errors "errors"
	io "io"
	net "net"
	strings "strings"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLocalInfileFraming(t *testing.T) {
	for _, framing := range []string{testCompression, testOB20} {
		t.Run(framing, func(t *testing.T) {
			payload := bytes.Repeat([]byte{0, 1, 255, 3}, 40000)
			db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
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
				if !testpeer.SendPackets(ctx, t, p, []byte{0, 2, 0, 2, 0, 0, 0}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			}, func(cfg *oceanbase.Config) {
				if framing == testCompression {
					cfg.Protocol.Compression = oceanbase.FeatureRequired
				} else {
					cfg.Protocol.OB20 = oceanbase.FeatureRequired
				}
			})
			defer func() { _ = db.Close() }()
			if _, err := oceanbase.Import(context.Background(), db, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: bytes.NewReader(payload)}); err != nil {
				t.Fatal(err)
			}
			if err := db.Ping(); err != nil {
				t.Fatal("framed response not fully consumed", err)
			}
		})
	}
}

func TestLocalInfileFinalResultsetRejected(t *testing.T) {
	db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !localRequest(ctx, t, p, localInputName) {
			return
		}
		_ = readLocalUpload(ctx, t, p)
		// A plausible rowset is still not an import acknowledgment.
		if !testpeer.SendPackets(ctx, t, p, []byte{1}) {
			return
		}
		for _, data := range [][]byte{testpeer.TextColumn("v", 253, 45, 0), {254, 0, 0, 2, 0}, {1, 'x'}, {254, 0, 0, 2, 0}} {
			if err := p.WriteMessage(ctx, data); err != nil {
				return
			}
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	if _, err := oceanbase.Import(context.Background(), db, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: strings.NewReader("x")}); !errors.Is(err, wire.ErrMalformed) {
		t.Fatal("non-OK/ERR import acknowledgment accepted", err)
	}
}

func TestLocalInfileSequenceWrap(t *testing.T) {
	payload := bytes.Repeat([]byte{3, 's'}, 8<<20)
	db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !localRequest(ctx, t, p, localInputName) {
			return
		}
		if !bytes.Equal(readLocalUpload(ctx, t, p), payload) {
			t.Error("large import corruption")
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if _, err := oceanbase.Import(context.Background(), db, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: bytes.NewReader(payload), MaxBytes: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalInfileMySQLXASessionRefused(t *testing.T) {
	for _, ended := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "ended"}[ended], func(t *testing.T) {
			db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+testXAStartSQL), 2) {
					return
				}
				if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+testXAEndSQL), 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03XA ROLLBACK X'67',X'',0x1"), 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			c, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			x := oceanbase.XID{FormatID: 1, GlobalID: []byte("g")}
			if err := oceanbase.XAStart(context.Background(), c, x, oceanbase.XANoFlags); err != nil {
				t.Fatal(err)
			}
			if ended {
				if err := oceanbase.XAEnd(context.Background(), c, x, oceanbase.XASuccess); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := oceanbase.Import(context.Background(), c, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: strings.NewReader("x")}); !errors.Is(err, oceanbase.ErrUnsupported) {
				t.Fatal(err)
			}
			if !ended {
				if err := oceanbase.XAEnd(context.Background(), c, x, oceanbase.XASuccess); err != nil {
					t.Fatal(err)
				}
			}
			if err := oceanbase.XARollback(context.Background(), c, x); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalInfileMySQLManualTransactionRefused(t *testing.T) {
	for _, command := range []string{"SET autocommit=0", testManualTxStart} {
		t.Run(command, func(t *testing.T) {
			status := byte(0)
			if command == testManualTxStart {
				status = 3
			}
			db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+command), status) || !testpeer.ServeOK(ctx, t, p, []byte("\x03ROLLBACK"), 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
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
			if _, err := oceanbase.Import(context.Background(), c, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: strings.NewReader("x")}); !errors.Is(err, oceanbase.ErrUnsupported) {
				t.Fatal(err)
			}
			if _, err := c.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalInfileMySQLTransactionRefused(t *testing.T) {
	var reads atomic.Int32
	db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testMySQLReadWrite, Status: 3}, {Query: testRollbackSQL, Status: 2}}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = oceanbase.Import(context.Background(), tx, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: longReaderFunc(func([]byte) (int, error) { reads.Add(1); return 0, io.EOF })})
	if !errors.Is(err, oceanbase.ErrUnsupported) || !errors.Is(err, oceanbase.ErrLocalInfile) || reads.Load() != 0 {
		t.Fatalf("unsafe transaction import: reads=%d err=%v", reads.Load(), err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalInfileOracleTransactionRefused(t *testing.T) {
	db := localInfileDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testAutocommitOff, Status: 4}, {Query: testOracleReadWrite, Status: 5}, {Query: testRollbackSQL, Status: 4}, {Query: testAutocommitOn, Status: 6}}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	reader := strings.NewReader("1\tx\n")
	if _, err = oceanbase.Import(context.Background(), tx, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: reader}); !errors.Is(err, oceanbase.ErrUnsupported) || reader.Len() != 4 {
		t.Fatal("Oracle transactional import sent SQL or consumed input", err, reader.Len())
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal("refused Oracle import damaged the session", err)
	}
}

func TestLocalInfileServerResponse(t *testing.T) {
	for _, stage := range []string{localErrorStage, "repeat", localMalformedNameStage, "malformed-final"} {
		t.Run(stage, func(t *testing.T) {
			var opened atomic.Int32
			db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
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
						if !testpeer.SendPackets(ctx, t, p, []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
						return
					case "repeat":
						if !testpeer.SendPackets(ctx, t, p, append([]byte{251}, localInputName...)) {
							return
						}
					case "malformed-final":
						if !testpeer.SendPackets(ctx, t, p, []byte{0}) {
							return
						}
					}
				}
				if b, err := p.ReadMessage(ctx); err == nil {
					t.Error("invalid import retained stream", b)
				}
			})
			defer func() { _ = db.Close() }()
			_, err := oceanbase.Import(context.Background(), db, localImportSQL, &oceanbase.LocalInfile{Open: func(context.Context, string) (io.ReadCloser, error) {
				opened.Add(1)
				return io.NopCloser(strings.NewReader("x")), nil
			}})
			var server *oceanbase.Error
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

func TestLocalInfileTimeout(t *testing.T) {
	for _, stage := range []string{localReaderStage, "ack"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := localInfileDB(t, oceanbase.ModeMySQL, func(peerCtx context.Context, p *wire.PacketIO) {
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
			input := &oceanbase.LocalInfile{Name: localInputName, Reader: bytes.NewBufferString(testLOBABC), Timeout: 200 * time.Millisecond}
			want := context.DeadlineExceeded
			if stage == localReaderStage {
				want = context.Canceled
				input.Reader = longReaderFunc(func([]byte) (int, error) { cancel(); return 0, io.EOF })
			}
			if _, err := oceanbase.Import(ctx, db, localImportSQL, input); !errors.Is(err, want) {
				t.Fatal("upload cancellation", err)
			}
		})
	}
}

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
			db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
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
				if !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 0, 0}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			calls := 0
			_, err := oceanbase.Import(context.Background(), db, localImportSQL, &oceanbase.LocalInfile{Open: func(ctx context.Context, name string) (io.ReadCloser, error) {
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
			if (ok && err != nil) || (!ok && !errors.Is(err, oceanbase.ErrLocalInfile)) || calls != 1 || reader.closed != wantClosed {
				t.Fatal("resolver lifecycle", calls, reader.closed, err)
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
		{"retry", &longErrorReader{err: driver.ErrBadConn}, 0, oceanbase.ErrLocalInfile},
		{"idle", longReaderFunc(func([]byte) (int, error) { return 0, nil }), 0, io.ErrNoProgress},
		{"invalid-count", longReaderFunc(func(b []byte) (int, error) { return len(b) + 1, nil }), 0, oceanbase.ErrLongData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dialed atomic.Int32
			db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
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
			}, func(cfg *oceanbase.Config) {
				dial := cfg.DialContext
				cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					dialed.Add(1)
					return dial(ctx, network, addr)
				}
			})
			defer func() { _ = db.Close() }()
			_, err := oceanbase.Import(context.Background(), db, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: tc.reader, MaxBytes: tc.max})
			if !errors.Is(err, tc.want) || errors.Is(err, driver.ErrBadConn) || dialed.Load() != 1 {
				t.Fatal("source error/replay", dialed.Load(), err)
			}
		})
	}
}

func TestLocalInfileFailure(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsolicited", true: "wrong-name"}[authorized], func(t *testing.T) {
			var reads atomic.Int32
			db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
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
				_, err = oceanbase.Import(context.Background(), db, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: longReaderFunc(func([]byte) (int, error) { reads.Add(1); return 0, io.EOF })})
			} else {
				_, err = db.ExecContext(context.Background(), localImportSQL)
			}
			if !errors.Is(err, oceanbase.ErrLocalInfile) || reads.Load() != 0 {
				t.Fatal("unauthorized source accessed", reads.Load(), err)
			}
		})
	}
}

func TestLocalInfilePolicy(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		t.Run(map[oceanbase.Mode]string{oceanbase.ModeMySQL: "mysql-mode", oceanbase.ModeOracle: "oracle-mode"}[mode], func(t *testing.T) {
			payload := bytes.Repeat([]byte{0, 1, 255, 10}, 20000)
			db := localInfileDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if !localRequest(ctx, t, p, localInputName) {
					return
				}
				if !bytes.Equal(readLocalUpload(ctx, t, p), payload) {
					t.Error("upload was altered")
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{0, 5, 0, 2, 0, 0, 0}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			reader := &longTestReader{Reader: bytes.NewReader(payload)}
			input := &oceanbase.LocalInfile{Name: localInputName, Reader: reader}
			result, err := oceanbase.Import(context.Background(), db, localImportSQL, input)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := result.RowsAffected(); err != nil || n != 5 || reader.closed != 0 {
				t.Fatal("result or Reader ownership", n, reader.closed, err)
			}
			if _, err = oceanbase.Import(context.Background(), db, localImportSQL, input); !errors.Is(err, oceanbase.ErrLocalInfile) {
				t.Fatal("input reused", err)
			}
			if err = db.Ping(); err != nil {
				t.Fatal("upload left unread response", err)
			}
		})
	}
}

func TestLocalInfileOracleManualTransactionRefused(t *testing.T) {
	for _, state := range []struct {
		command string
		status  byte
	}{{command: testAutocommitOff, status: 4}, {command: testOracleReadWrite, status: 7}} {
		t.Run(state.command, func(t *testing.T) {
			db := localInfileDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+state.command), state.status) || !testpeer.ServeOK(ctx, t, p, []byte("\x03ROLLBACK"), 6) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
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
			if _, err = oceanbase.Import(context.Background(), c, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: reader}); !errors.Is(err, oceanbase.ErrUnsupported) || reader.Len() != 1 {
				t.Fatal("manual Oracle transaction allowed import", err, reader.Len())
			}
			if _, err = c.ExecContext(context.Background(), "ROLLBACK"); err != nil {
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
			db := localInfileDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			reader := strings.NewReader("x")
			_, err := oceanbase.Import(context.Background(), db, query, &oceanbase.LocalInfile{Name: localInputName, Reader: reader})
			if !errors.Is(err, oceanbase.ErrUnsupported) || reader.Len() != 1 {
				t.Fatal("unsafe Oracle command sent or input consumed", err, reader.Len())
			}
			if err = db.PingContext(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
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
			db := localInfileDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			reader := strings.NewReader("x")
			_, err := oceanbase.Import(context.Background(), db, query, &oceanbase.LocalInfile{Name: localInputName, Reader: reader})
			if !errors.Is(err, oceanbase.ErrUnsupported) || !errors.Is(err, oceanbase.ErrLocalInfile) || reader.Len() != 1 {
				t.Fatal("unsafe command sent or input consumed", err, reader.Len())
			}
			if err = db.PingContext(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
