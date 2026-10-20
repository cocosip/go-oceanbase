package xa_test

import (
	bytes "bytes"
	context "context"
	binary "encoding/binary"
	errors "errors"
	fmt "fmt"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testXAServerPath = "xa-server"
	testXAStartSQL   = "XA START X'67',X'',0x1"
	testXAEndSQL     = "XA END X'67',X'',0x1"
	testXAIdentity   = "xa_identity"
)

func TestXAState(t *testing.T) {
	xid := oceanbase.XID{FormatID: 42, GlobalID: []byte{0, 255}, BranchID: []byte("b")}
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		for _, query := range []string{
			"XA START X'00ff',X'62',0x2a",
			"XA END X'00ff',X'62',0x2a SUSPEND",
			"XA START X'00ff',X'62',0x2a RESUME",
			"XA END X'00ff',X'62',0x2a",
			"XA PREPARE X'00ff',X'62',0x2a",
			"XA COMMIT X'00ff',X'62',0x2a",
		} {
			if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+query), 2) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if err = oceanbase.XAStart(ctx, pinned, xid, oceanbase.XANoFlags); err != nil {
		t.Fatal(err)
	}
	if _, err = pinned.BeginTx(ctx, nil); !errors.Is(err, oceanbase.ErrXA) {
		t.Fatalf("local transaction entered XA session: %v", err)
	}
	if err = oceanbase.XAEnd(ctx, pinned, xid, oceanbase.XASuspend); err != nil {
		t.Fatal(err)
	}
	if err = oceanbase.XAStart(ctx, pinned, xid, oceanbase.XAResume); err != nil {
		t.Fatal(err)
	}
	if err = oceanbase.XAEnd(ctx, pinned, xid, oceanbase.XASuccess); err != nil {
		t.Fatal(err)
	}
	if vote, e := oceanbase.XAPrepare(ctx, pinned, xid); e != nil || vote != oceanbase.XAPrepared {
		t.Fatal(vote, e)
	}
	if err = oceanbase.XACommit(ctx, pinned, xid, false); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
		t.Fatal("XA session returned to ordinary pool", err)
	}
}

func TestXAEndFailAndOnePhase(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			x := oceanbase.XID{FormatID: 1, GlobalID: []byte("g")}
			finish := "XA COMMIT X'67',X'',0x1 ONE PHASE"
			if fail {
				finish = "XA ROLLBACK X'67',X'',0x1"
			}
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				queries := []string{testXAStartSQL, testXAEndSQL}
				if !fail {
					queries = append(queries, testXAStartSQL+" JOIN", testXAEndSQL)
				}
				queries = append(queries, finish)
				for _, q := range queries {
					if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+q), 2) {
						return
					}
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, e := db.Conn(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = pinned.Close() }()
			if e = oceanbase.XAStart(ctx, pinned, x, oceanbase.XANoFlags); e != nil {
				t.Fatal(e)
			}
			if e = oceanbase.XAStart(ctx, pinned, x, oceanbase.XAJoin); !errors.Is(e, oceanbase.ErrXA) {
				t.Fatal(e)
			}
			if e = oceanbase.SetSchema(ctx, pinned, testXAIdentity); !errors.Is(e, dberrors.ErrUnsupported) {
				t.Fatal(e)
			}
			if e = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: testXAIdentity}, ""); !errors.Is(e, dberrors.ErrUnsupported) {
				t.Fatal(e)
			}
			flag := oceanbase.XASuccess
			if fail {
				flag = oceanbase.XAFail
			}
			if e = oceanbase.XAEnd(ctx, pinned, x, flag); e != nil {
				t.Fatal(e)
			}
			if !fail {
				if e = oceanbase.XAStart(ctx, pinned, x, oceanbase.XAJoin); e != nil {
					t.Fatal(e)
				}
				if e = oceanbase.XAEnd(ctx, pinned, x, oceanbase.XASuccess); e != nil {
					t.Fatal(e)
				}
			}
			if e = oceanbase.XAForget(ctx, pinned, x); !errors.Is(e, dberrors.ErrUnsupported) {
				t.Fatal(e)
			}
			if fail {
				_, e = oceanbase.XAPrepare(ctx, pinned, x)
				var xe *oceanbase.XAError
				if !errors.As(e, &xe) || xe.Code != 100 {
					t.Fatal(e)
				}
				if e = oceanbase.XACommit(ctx, pinned, x, true); !errors.Is(e, oceanbase.ErrXA) {
					t.Fatal(e)
				}
				e = oceanbase.XARollback(ctx, pinned, x)
			} else {
				e = oceanbase.XACommit(ctx, pinned, x, true)
			}
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}

func serveXAReturn(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, params []wire.Parameter, code int64, status byte, tailError bool) bool {
	t.Helper()
	want, e := wire.EncodePrepareExecute(0, []byte(query), params, 4096)
	if e != nil || !testpeer.ReadCommand(ctx, t, p, want) {
		return false
	}
	packets := [][]byte{combinedHeader(71, 1, uint16(len(params)), 1)}
	for range params {
		packets = append(packets, testpeer.TextColumn("p", 6, 63, 0))
	}
	packets = append(packets, []byte{254, 0, 0, status, 0}, testpeer.TextColumn("code", 8, 63, 0), []byte{254, 0, 0, status, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, uint64(code)), []byte{254, 0, 0, status, 0})
	tail := []byte{0, 0, 0, status, 0, 0, 0}
	if tailError {
		tail = []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}
	}
	packets = append(packets, tail)
	return testpeer.SendPackets(ctx, t, p, packets...) && testpeer.ReadCommand(ctx, t, p, stmtPacket(25, 71))
}

func TestXAOracle(t *testing.T) {
	for _, vote := range []int64{0, 3, -4, 100} {
		t.Run(fmt.Sprint(vote), func(t *testing.T) {
			x := oceanbase.XID{FormatID: 42, GlobalID: []byte{0, 255}, BranchID: []byte("b")}
			param := nativeXID(x)
			if !bytes.Equal(param.TypeMetadata, []byte{3, 'S', 'Y', 'S', 11, 'D', 'B', 'M', 'S', '_', 'X', 'A', '_', 'X', 'I', 'D', 0}) || !bytes.Equal(param.Data, []byte{0, 2, '4', '2', 2, 0, 255, 1, 'b'}) {
				t.Fatal(param)
			}
			db := outOracleDB(t, config.PrepareClient, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=0"), 4) || !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_START(?, ?); END;", []wire.Parameter{{Type: 6}, param, lobInteger(0)}, 0, 4, false) {
					return
				}
				if !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_END(?, ?); END;", []wire.Parameter{{Type: 6}, param, lobInteger(int64(oceanbase.XASuccess))}, 0, 4, false) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=1"), 6) {
					return
				}
				if !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_PREPARE(?); END;", []wire.Parameter{{Type: 6}, param}, vote, 6, false) {
					return
				}
				if vote >= 0 && vote < 100 {
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				}
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, e := db.Conn(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = pinned.Close() }()
			if e = oceanbase.XAStart(ctx, pinned, x, oceanbase.XANoFlags); e != nil {
				t.Fatal(e)
			}
			if e = oceanbase.XAEnd(ctx, pinned, x, oceanbase.XASuccess); e != nil {
				t.Fatal(e)
			}
			got, e := oceanbase.XAPrepare(ctx, pinned, x)
			if vote == 0 || vote == 3 {
				if e != nil || int64(got) != vote {
					t.Fatal(got, e)
				}
			} else {
				var xe *oceanbase.XAError
				if !errors.As(e, &xe) || int64(xe.Code) != vote {
					t.Fatal(got, e)
				}
			}
		})
	}
}

func TestXAOracleTailUnknown(t *testing.T) {
	x := oceanbase.XID{FormatID: 1, GlobalID: []byte("g")}
	db := outOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		_ = serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_COMMIT(?, ?); END;", []wire.Parameter{{Type: 6}, nativeXID(x), {Type: 1, Data: []byte{0}}}, 0, 6, true)
	})
	defer func() { _ = db.Close() }()
	pinned, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = pinned.Close() }()
	e = oceanbase.XACommit(context.Background(), pinned, x, false)
	if !errors.Is(e, oceanbase.ErrCommitUnknown) || !errors.Is(e, oceanbase.ErrXAOutcomeUnknown) {
		t.Fatal(e)
	}
}

func TestXAOracleExtraResult(t *testing.T) {
	x := oceanbase.XID{FormatID: 1, GlobalID: []byte("g")}
	db := outOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		want, e := wire.EncodePrepareExecute(0, []byte("BEGIN ? := SYS.DBMS_XA.XA_COMMIT(?, ?); END;"), []wire.Parameter{{Type: 6}, nativeXID(x), {Type: 1, Data: []byte{0}}}, 4096)
		if e != nil || !testpeer.ReadCommand(ctx, t, p, want) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, combinedHeader(71, 1, 3, 1), testpeer.TextColumn("p", 6, 63, 0), testpeer.TextColumn("p", 6, 63, 0), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("code", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 0), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 14, 0, 0, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, stmtPacket(25, 71)) {
			return
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	pinned, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = pinned.Close() }()
	e = oceanbase.XACommit(context.Background(), pinned, x, false)
	if !errors.Is(e, oceanbase.ErrCommitUnknown) {
		t.Fatal("extra native result accepted", e)
	}
}

func TestXAPreparedClose(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		for _, q := range []string{testXAStartSQL, testXAEndSQL, "XA PREPARE X'67',X'',0x1"} {
			if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+q), 2) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, e := db.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = pinned.Close() }()
	x := oceanbase.XID{FormatID: 1, GlobalID: []byte("g")}
	if e = oceanbase.XAStart(ctx, pinned, x, oceanbase.XANoFlags); e != nil {
		t.Fatal(e)
	}
	if e = oceanbase.XAEnd(ctx, pinned, x, oceanbase.XASuccess); e != nil {
		t.Fatal(e)
	}
	if _, e = oceanbase.XAPrepare(ctx, pinned, x); e != nil {
		t.Fatal(e)
	}
	if e = pinned.Close(); e != nil || db.Stats().Idle != 0 {
		t.Fatal(e, db.Stats())
	}
}

func TestXAOracleRecoverProtocol(t *testing.T) {
	db := outOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		query := "DECLARE x SYS.DBMS_XA_XID_ARRAY := SYS.DBMS_XA_XID_ARRAY(); BEGIN x := SYS.DBMS_XA.XA_RECOVER(); ? := x; END;"
		want, e := wire.EncodePrepareExecute(0, []byte(query), []wire.Parameter{{Type: 6}}, 4096)
		if e != nil || !testpeer.ReadCommand(ctx, t, p, want) {
			return
		}
		column := append(testpeer.TextColumn("x", 160, 63, 0), 0, 0, 0, 160, 3, 'S', 'Y', 'S', 11, 'D', 'B', 'M', 'S', '_', 'X', 'A', '_', 'X', 'I', 'D', 1)
		row := []byte{0, 0, 1, 0, 0, 2, '4', '2', 2, 0, 255, 1, 'b'}
		if !testpeer.SendPackets(ctx, t, p, combinedHeader(71, 1, 1, 1), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, column, []byte{254, 0, 0, 6, 16}, row, []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, stmtPacket(25, 71)) {
			return
		}
		if !serveDescriptorRows(ctx, t, p, oceanbase.MetadataTypes, xaSchema, xaXIDType, [][]string{{"", xaSchema, xaXIDType, "OBJECT", "3", "1", "1"}}) || !serveDescriptorRows(ctx, t, p, oceanbase.MetadataTypeAttributes, xaSchema, xaXIDType, [][]string{{"", xaSchema, xaXIDType, "FORMATID", "1", valueformat.TypeNameNumber, "", "", "", ""}, {"", xaSchema, xaXIDType, "GTRID", "2", valueformat.TypeNameRaw, "", "", "", ""}, {"", xaSchema, xaXIDType, "BQUAL", "3", valueformat.TypeNameRaw, "", "", "", ""}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = pinned.Close() }()
	got, e := oceanbase.XARecover(context.Background(), pinned)
	if e != nil || len(got) != 1 || got[0].FormatID != 42 || !bytes.Equal(got[0].GlobalID, []byte{0, 255}) || string(got[0].BranchID) != "b" {
		t.Fatal(got, e)
	}
}

func TestXARecoveryResponse(t *testing.T) {
	for _, path := range []string{"xa-null", "bad-length", "extra", "leading-ok", "no-rowset", "xa-limit", testXAServerPath} {
		t.Run(path, func(t *testing.T) {
			db := localInfileDB(t, config.ModeMySQL, false, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, "XA RECOVER") {
					return
				}
				if path == testXAServerPath {
					if !testpeer.SendPackets(ctx, t, p, []byte{255, 0x75, 5, '#', 'X', 'A', 'E', '0', '0', 'x'}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
					return
				}
				if path == "no-rowset" {
					_ = p.WriteMessage(ctx, commandPacket(0, 0, 2))
					_, _ = p.ReadMessage(ctx)
					return
				}
				if path == "leading-ok" && !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 10)) {
					return
				}
				for _, packet := range [][]byte{{4}, testpeer.TextColumn("f", 3, 63, 0), testpeer.TextColumn("g", 3, 63, 0), testpeer.TextColumn("b", 3, 63, 0), testpeer.TextColumn("d", 252, 63, 0), {254, 0, 0, 2, 0}} {
					if e := p.WriteMessage(ctx, packet); e != nil {
						return
					}
				}
				row := []byte{1, '1', 1, '1', 1, '0', 1, 'g'}
				if path == "xa-null" {
					row = []byte{251, 1, '1', 1, '0', 1, 'g'}
				}
				if path == "bad-length" {
					row = []byte{1, '1', 1, '2', 1, '0', 1, 'g'}
				}
				if e := p.WriteMessage(ctx, row); e != nil {
					return
				}
				if path == "xa-limit" {
					if e := p.WriteMessage(ctx, row); e != nil {
						return
					}
				}
				status := byte(2)
				if path == "extra" {
					status = 10
				}
				_ = p.WriteMessage(ctx, []byte{254, 0, 0, status, 0})
				_, _ = p.ReadMessage(ctx)
			}, func(cfg *config.Config) { cfg.Limits.MaxResultItems = 1 })
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, e := db.Conn(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = pinned.Close() }()
			got, e := oceanbase.XARecover(ctx, pinned)
			if e == nil || got != nil {
				t.Fatal(got, e)
			}
			if path == testXAServerPath {
				var xe *oceanbase.XAError
				if !errors.As(e, &xe) || xe.Code != -4 {
					t.Fatal(e)
				}
				if e = pinned.PingContext(ctx); e != nil {
					t.Fatal(e)
				}
			} else if e = pinned.PingContext(ctx); e == nil {
				t.Fatal("malformed recovery remained usable")
			}
		})
	}
}

func TestXAFraming(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, framing := range []string{testCompression, testOB20} {
			t.Run(string(mode)+"/"+framing, func(t *testing.T) {
				x := oceanbase.XID{FormatID: 1, GlobalID: []byte("g")}
				db := localInfileDB(t, mode, false, func(ctx context.Context, p *wire.PacketIO) {
					if !p.Framed() {
						t.Error("unframed XA")
						return
					}
					if mode == config.ModeOracle {
						param := nativeXID(x)
						if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=0"), 4) || !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_START(?, ?); END;", []wire.Parameter{{Type: 6}, param, lobInteger(0)}, 0, 4, false) {
							return
						}
						if !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_END(?, ?); END;", []wire.Parameter{{Type: 6}, param, lobInteger(int64(oceanbase.XASuccess))}, 0, 4, false) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=1"), 6) {
							return
						}
						if !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_ROLLBACK(?); END;", []wire.Parameter{{Type: 6}, param}, 0, 6, false) {
							return
						}
					} else {
						for _, q := range []string{testXAStartSQL, testXAEndSQL, "XA ROLLBACK X'67',X'',0x1"} {
							if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+q), 2) {
								return
							}
						}
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				}, func(cfg *config.Config) {
					if framing == testCompression {
						cfg.Protocol.Compression = config.FeatureRequired
					} else {
						cfg.Protocol.OB20 = config.FeatureRequired
					}
				})
				defer func() { _ = db.Close() }()
				ctx := context.Background()
				pinned, e := db.Conn(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = pinned.Close() }()
				if e = oceanbase.XAStart(ctx, pinned, x, oceanbase.XANoFlags); e != nil {
					t.Fatal(e)
				}
				if e = oceanbase.XAEnd(ctx, pinned, x, oceanbase.XASuccess); e != nil {
					t.Fatal(e)
				}
				if e = oceanbase.XARollback(ctx, pinned, x); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}

func TestXARecover(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "XA RECOVER") || !testpeer.SendPackets(ctx, t, p, []byte{4}, testpeer.TextColumn("formatID", 3, 63, 0), testpeer.TextColumn("gtrid_length", 3, 63, 0), testpeer.TextColumn("bqual_length", 3, 63, 0), testpeer.TextColumn("data", 252, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{2, '4', '2', 1, '2', 1, '1', 3, 0, 255, 'b'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	got, err := oceanbase.XARecover(context.Background(), pinned)
	if err != nil || len(got) != 1 || got[0].FormatID != 42 || !bytes.Equal(got[0].GlobalID, []byte{0, 255}) || string(got[0].BranchID) != "b" {
		t.Fatal(got, err)
	}
}

func TestXAUnknownCommit(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		_ = testpeer.ReadQuery(ctx, t, p, "XA COMMIT X'67',X'',0x1")
	})
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	err = oceanbase.XACommit(context.Background(), pinned, oceanbase.XID{FormatID: 1, GlobalID: []byte("g")}, false)
	if !errors.Is(err, oceanbase.ErrCommitUnknown) || !errors.Is(err, oceanbase.ErrXAOutcomeUnknown) {
		t.Fatal(err)
	}
}
