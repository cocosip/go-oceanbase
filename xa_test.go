package oceanbase

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/testutil"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testXAServerPath = "xa-server"
	testXAStartSQL   = "XA START X'67',X'',0x1"
	testXAEndSQL     = "XA END X'67',X'',0x1"
	testXAIdentity   = "xa_identity"
)

func TestXAState(t *testing.T) {
	xid := XID{FormatID: 42, GlobalID: []byte{0, 255}, BranchID: []byte("b")}
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		for _, query := range []string{
			"XA START X'00ff',X'62',0x2a",
			"XA END X'00ff',X'62',0x2a SUSPEND",
			"XA START X'00ff',X'62',0x2a RESUME",
			"XA END X'00ff',X'62',0x2a",
			"XA PREPARE X'00ff',X'62',0x2a",
			"XA COMMIT X'00ff',X'62',0x2a",
		} {
			if !serveOK(ctx, t, p, []byte("\x03"+query), 2) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if err = XAStart(ctx, pinned, xid, XANoFlags); err != nil {
		t.Fatal(err)
	}
	if _, err = pinned.BeginTx(ctx, nil); !errors.Is(err, ErrXA) {
		t.Fatalf("local transaction entered XA session: %v", err)
	}
	if err = XAEnd(ctx, pinned, xid, XASuspend); err != nil {
		t.Fatal(err)
	}
	if err = XAStart(ctx, pinned, xid, XAResume); err != nil {
		t.Fatal(err)
	}
	if err = XAEnd(ctx, pinned, xid, XASuccess); err != nil {
		t.Fatal(err)
	}
	if vote, e := XAPrepare(ctx, pinned, xid); e != nil || vote != XAPrepared {
		t.Fatal(vote, e)
	}
	if err = XACommit(ctx, pinned, xid, false); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
		t.Fatal("XA session returned to ordinary pool", err)
	}
}

func TestXAValidation(t *testing.T) {
	x := XID{FormatID: 7, GlobalID: []byte("g")}
	if err := XAStart(context.Background(), nil, x, XANoFlags); !errors.Is(err, ErrXA) {
		t.Fatal(err)
	}
	c := &conn{cfg: Config{Limits: Limits{MaxResultItems: 2}}, session: &handshakeSession{mode: ModeMySQL, ok: wire.OK{Status: 2}}}
	for _, flags := range []XAFlags{XASuccess, XAFail, XAJoin | XAResume, -1} {
		if err := c.validateXALocked(&xaRequest{op: xaStart, xid: x, flags: flags}); !errors.Is(err, ErrXA) {
			t.Fatal(flags, err)
		}
	}
	for _, invalid := range []XID{{}, {FormatID: -1, GlobalID: []byte("g")}, {GlobalID: make([]byte, 65)}, {GlobalID: []byte("g"), BranchID: make([]byte, 65)}} {
		if validXID(invalid) {
			t.Fatal("accepted invalid XID", invalid)
		}
	}
	copyX := cloneXID(x)
	x.GlobalID[0] = 'x'
	if string(copyX.GlobalID) != "g" {
		t.Fatal("XID alias")
	}
	if mysqlXID(XID{FormatID: -2, GlobalID: []byte{0, 255}}) != "X'00ff',X'',0xfffffffe" {
		t.Fatal("negative format encoding")
	}
	if _, err := XARecover(context.Background(), nil); err == nil {
		t.Fatal("nil recovery connection")
	}
}

func TestXAEndFailAndOnePhase(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			x := XID{FormatID: 1, GlobalID: []byte("g")}
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
					if !serveOK(ctx, t, p, []byte("\x03"+q), 2) {
						return
					}
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, e := db.Conn(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = pinned.Close() }()
			if e = XAStart(ctx, pinned, x, XANoFlags); e != nil {
				t.Fatal(e)
			}
			if e = XAStart(ctx, pinned, x, XAJoin); !errors.Is(e, ErrXA) {
				t.Fatal(e)
			}
			if e = SetSchema(ctx, pinned, testXAIdentity); !errors.Is(e, ErrUnsupported) {
				t.Fatal(e)
			}
			if e = ChangeUser(ctx, pinned, Credentials{User: testXAIdentity}, ""); !errors.Is(e, ErrUnsupported) {
				t.Fatal(e)
			}
			flag := XASuccess
			if fail {
				flag = XAFail
			}
			if e = XAEnd(ctx, pinned, x, flag); e != nil {
				t.Fatal(e)
			}
			if !fail {
				if e = XAStart(ctx, pinned, x, XAJoin); e != nil {
					t.Fatal(e)
				}
				if e = XAEnd(ctx, pinned, x, XASuccess); e != nil {
					t.Fatal(e)
				}
			}
			if e = XAForget(ctx, pinned, x); !errors.Is(e, ErrUnsupported) {
				t.Fatal(e)
			}
			if fail {
				_, e = XAPrepare(ctx, pinned, x)
				var xe *XAError
				if !errors.As(e, &xe) || xe.Code != 100 {
					t.Fatal(e)
				}
				if e = XACommit(ctx, pinned, x, true); !errors.Is(e, ErrXA) {
					t.Fatal(e)
				}
				e = XARollback(ctx, pinned, x)
			} else {
				e = XACommit(ctx, pinned, x, true)
			}
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestXARecoveryValidation(t *testing.T) {
	for _, vals := range [][]driver.Value{
		{int64(-1), int64(1), int64(0), []byte("g")}, {int64(1), int64(0), int64(0), []byte{}},
		{int64(1), int64(1), int64(1), []byte("g")}, {int64(1), int64(1), nil, []byte("g")},
		{int64(1), int64(1), int64(0), nil}, {"2147483648", "1", "0", "g"}, {"1.2", "1", "0", "g"},
	} {
		if _, e := decodeRecoveredXID(vals); e == nil {
			t.Fatal(vals)
		}
	}
	vals := []driver.Value{"-2", "1", "0", []byte{0xff}}
	x, e := decodeRecoveredXID(vals)
	if e != nil || x.FormatID != -2 {
		t.Fatal(x, e)
	}
	vals[3].([]byte)[0] = 0
	if x.GlobalID[0] != 0xff {
		t.Fatal("recovery data alias")
	}
}

func serveXAReturn(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, params []wire.Parameter, code int64, status byte, tailError bool) bool {
	t.Helper()
	want, e := wire.EncodePrepareExecute(0, []byte(query), params, 4096)
	if e != nil || !readCommand(ctx, t, p, want) {
		return false
	}
	packets := [][]byte{combinedHeader(71, 1, uint16(len(params)), 1)}
	for range params {
		packets = append(packets, textColumn("p", 6, 63, 0))
	}
	packets = append(packets, []byte{254, 0, 0, status, 0}, textColumn("code", 8, 63, 0), []byte{254, 0, 0, status, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, uint64(code)), []byte{254, 0, 0, status, 0})
	tail := []byte{0, 0, 0, status, 0, 0, 0}
	if tailError {
		tail = []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}
	}
	packets = append(packets, tail)
	return sendPackets(ctx, t, p, packets...) && readCommand(ctx, t, p, stmtPacket(25, 71))
}

func TestXAOracle(t *testing.T) {
	for _, vote := range []int64{0, 3, -4, 100} {
		t.Run(fmt.Sprint(vote), func(t *testing.T) {
			x := XID{FormatID: 42, GlobalID: []byte{0, 255}, BranchID: []byte("b")}
			param := nativeXID(x)
			if !bytes.Equal(param.TypeMetadata, []byte{3, 'S', 'Y', 'S', 11, 'D', 'B', 'M', 'S', '_', 'X', 'A', '_', 'X', 'I', 'D', 0}) || !bytes.Equal(param.Data, []byte{0, 2, '4', '2', 2, 0, 255, 1, 'b'}) {
				t.Fatal(param)
			}
			db := outOracleDB(t, PrepareClient, func(ctx context.Context, p *wire.PacketIO) {
				if !serveOK(ctx, t, p, []byte("\x03SET autocommit=0"), 4) || !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_START(?, ?); END;", []wire.Parameter{{Type: 6}, param, lobInteger(0)}, 0, 4, false) {
					return
				}
				if !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_END(?, ?); END;", []wire.Parameter{{Type: 6}, param, lobInteger(int64(XASuccess))}, 0, 4, false) || !serveOK(ctx, t, p, []byte("\x03SET autocommit=1"), 6) {
					return
				}
				if !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_PREPARE(?); END;", []wire.Parameter{{Type: 6}, param}, vote, 6, false) {
					return
				}
				if vote >= 0 && vote < 100 {
					p.ResetSequence()
					serveQuit(t, p)
				}
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, e := db.Conn(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = pinned.Close() }()
			if e = XAStart(ctx, pinned, x, XANoFlags); e != nil {
				t.Fatal(e)
			}
			if e = XAEnd(ctx, pinned, x, XASuccess); e != nil {
				t.Fatal(e)
			}
			got, e := XAPrepare(ctx, pinned, x)
			if vote == 0 || vote == 3 {
				if e != nil || int64(got) != vote {
					t.Fatal(got, e)
				}
			} else {
				var xe *XAError
				if !errors.As(e, &xe) || int64(xe.Code) != vote {
					t.Fatal(got, e)
				}
			}
		})
	}
}

func TestXAOracleRecoveryShape(t *testing.T) {
	x := Object{Schema: xaSchema, Type: xaXIDType, Fields: []Attribute{{Name: "FORMATID", Type: typeNameNumber, Value: Number("42")}, {Name: "GTRID", Type: typeNameRaw, Value: Raw{0, 255}}, {Name: "BQUAL", Type: typeNameRaw, Value: Raw("b")}}}
	for _, named := range []bool{false, true} {
		a := Array{Schema: xaSchema, Type: xaArrayType, ElementType: xaXIDType, Elements: []any{x}}
		if !named {
			a.Schema, a.Type = "", ""
		}
		v, e := a.Value()
		if e != nil {
			t.Fatal(e)
		}
		got, e := decodeOracleRecovery([]driver.Value{v}, Limits{MaxResultItems: 2, MaxMessageBytes: 1024})
		if e != nil || len(got) != 1 || !reflect.DeepEqual(got[0], XID{FormatID: 42, GlobalID: []byte{0, 255}, BranchID: []byte("b")}) {
			t.Fatal(got, e)
		}
	}
	x.Fields[2].Value = nil
	v, e := (Array{Schema: xaSchema, Type: xaArrayType, ElementType: xaXIDType, Elements: []any{x}}).Value()
	if e != nil {
		t.Fatal(e)
	}
	got, e := decodeOracleRecovery([]driver.Value{v}, Limits{MaxResultItems: 2, MaxMessageBytes: 1024})
	if e != nil || len(got) != 1 || len(got[0].BranchID) != 0 {
		t.Fatal("NULL BQUAL must represent the legal empty branch qualifier", got, e)
	}
}

func TestXAOracleTailUnknown(t *testing.T) {
	x := XID{FormatID: 1, GlobalID: []byte("g")}
	db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		_ = serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_COMMIT(?, ?); END;", []wire.Parameter{{Type: 6}, nativeXID(x), {Type: 1, Data: []byte{0}}}, 0, 6, true)
	})
	defer func() { _ = db.Close() }()
	pinned, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = pinned.Close() }()
	e = XACommit(context.Background(), pinned, x, false)
	if !errors.Is(e, ErrCommitUnknown) || !errors.Is(e, ErrXAOutcomeUnknown) {
		t.Fatal(e)
	}
}

func TestXAOracleExtraResult(t *testing.T) {
	x := XID{FormatID: 1, GlobalID: []byte("g")}
	db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		want, e := wire.EncodePrepareExecute(0, []byte("BEGIN ? := SYS.DBMS_XA.XA_COMMIT(?, ?); END;"), []wire.Parameter{{Type: 6}, nativeXID(x), {Type: 1, Data: []byte{0}}}, 4096)
		if e != nil || !readCommand(ctx, t, p, want) {
			return
		}
		if !sendPackets(ctx, t, p, combinedHeader(71, 1, 3, 1), textColumn("p", 6, 63, 0), textColumn("p", 6, 63, 0), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("code", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 0), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 14, 0, 0, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 71)) {
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
	e = XACommit(context.Background(), pinned, x, false)
	if !errors.Is(e, ErrCommitUnknown) {
		t.Fatal("extra native result accepted", e)
	}
}

func TestXARecoveredBranchLimit(t *testing.T) {
	c := &conn{xaUsed: true, session: &handshakeSession{mode: ModeMySQL}, cfg: Config{Limits: Limits{MaxResultItems: 1}}, xaBranches: map[string]xaBranch{testXAIdentity: {stage: xaPrepared}}}
	if e := c.validateXALocked(&xaRequest{op: xaPrepare, xid: XID{FormatID: 1, GlobalID: []byte("g")}}); e == nil {
		t.Fatal("unbounded recovered branch registration")
	}
}

func TestXAOracleFlags(t *testing.T) {
	x := XID{GlobalID: []byte("g")}
	c := &conn{xaUsed: true, session: &handshakeSession{mode: ModeOracle}, cfg: Config{Limits: Limits{MaxResultItems: 8}}, xaBranches: map[string]xaBranch{xidKey(x): {stage: xaPrepared}}}
	for _, flags := range []XAFlags{XAOracleReadOnly, XAOracleSerializable, XAOracleLoose} {
		if !validXAFlags(ModeOracle, &xaRequest{op: xaStart, flags: flags}) || validXAFlags(ModeMySQL, &xaRequest{op: xaStart, flags: flags}) {
			t.Fatal(flags)
		}
		if e := c.validateXALocked(&xaRequest{op: xaStart, xid: x, flags: flags}); !errors.Is(e, ErrXA) {
			t.Fatal("prepared branch restarted", flags, e)
		}
	}
	for _, flags := range []XAFlags{XAMigrate, XASuspend | XAMigrate} {
		if !validXAFlags(ModeOracle, &xaRequest{op: xaEnd, flags: flags}) || validXAFlags(ModeMySQL, &xaRequest{op: xaEnd, flags: flags}) {
			t.Fatal(flags)
		}
	}
}

func TestXAPreparedClose(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		for _, q := range []string{testXAStartSQL, testXAEndSQL, "XA PREPARE X'67',X'',0x1"} {
			if !serveOK(ctx, t, p, []byte("\x03"+q), 2) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, e := db.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = pinned.Close() }()
	x := XID{FormatID: 1, GlobalID: []byte("g")}
	if e = XAStart(ctx, pinned, x, XANoFlags); e != nil {
		t.Fatal(e)
	}
	if e = XAEnd(ctx, pinned, x, XASuccess); e != nil {
		t.Fatal(e)
	}
	if _, e = XAPrepare(ctx, pinned, x); e != nil {
		t.Fatal(e)
	}
	if e = pinned.Close(); e != nil || db.Stats().Idle != 0 {
		t.Fatal(e, db.Stats())
	}
}

func TestXALocalTransactionGuard(t *testing.T) {
	c := &conn{session: &handshakeSession{mode: ModeMySQL, ok: wire.OK{Status: 3}}, cfg: Config{Limits: Limits{MaxResultItems: 4}}}
	if e := c.validateXALocked(&xaRequest{op: xaStart, xid: XID{GlobalID: []byte("g")}}); !errors.Is(e, ErrXA) {
		t.Fatal(e)
	}
	c.session.ok.Status = 2
	c.activeTx = &tx{}
	if e := c.validateXALocked(&xaRequest{op: xaRecover}); !errors.Is(e, ErrXA) {
		t.Fatal(e)
	}
}

func TestXAOracleRecoverProtocol(t *testing.T) {
	db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		query := "DECLARE x SYS.DBMS_XA_XID_ARRAY := SYS.DBMS_XA_XID_ARRAY(); BEGIN x := SYS.DBMS_XA.XA_RECOVER(); ? := x; END;"
		want, e := wire.EncodePrepareExecute(0, []byte(query), []wire.Parameter{{Type: 6}}, 4096)
		if e != nil || !readCommand(ctx, t, p, want) {
			return
		}
		column := append(textColumn("x", 160, 63, 0), 0, 0, 0, 160, 3, 'S', 'Y', 'S', 11, 'D', 'B', 'M', 'S', '_', 'X', 'A', '_', 'X', 'I', 'D', 1)
		row := []byte{0, 0, 1, 0, 0, 2, '4', '2', 2, 0, 255, 1, 'b'}
		if !sendPackets(ctx, t, p, combinedHeader(71, 1, 1, 1), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, column, []byte{254, 0, 0, 6, 16}, row, []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 71)) {
			return
		}
		if !serveDescriptorRows(ctx, t, p, MetadataTypes, xaSchema, xaXIDType, [][]string{{"", xaSchema, xaXIDType, "OBJECT", "3", "1", "1"}}) || !serveDescriptorRows(ctx, t, p, MetadataTypeAttributes, xaSchema, xaXIDType, [][]string{{"", xaSchema, xaXIDType, "FORMATID", "1", typeNameNumber, "", "", "", ""}, {"", xaSchema, xaXIDType, "GTRID", "2", typeNameRaw, "", "", "", ""}, {"", xaSchema, xaXIDType, "BQUAL", "3", typeNameRaw, "", "", "", ""}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = pinned.Close() }()
	got, e := XARecover(context.Background(), pinned)
	if e != nil || len(got) != 1 || got[0].FormatID != 42 || !bytes.Equal(got[0].GlobalID, []byte{0, 255}) || string(got[0].BranchID) != "b" {
		t.Fatal(got, e)
	}
}

func TestXAPartialSend(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(string(mode), func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close(); _ = server.Close() }()
			fault := testutil.NewFaultConn(client, testutil.Faults{MaxWrite: 2, WriteFailAt: 2})
			c := &conn{mu: make(chan struct{}, 1), cfg: outputTestConfig(), session: &handshakeSession{raw: fault, conn: fault, packets: wire.NewPacketIO(fault, 4096), mode: mode, obVersion: testOutVersion, ok: wire.OK{Status: 2}}}
			done := make(chan struct{})
			go func() { defer close(done); _, _ = io.Copy(io.Discard, server) }()
			_, e := c.execXA(context.Background(), &xaRequest{op: xaCommit, xid: XID{FormatID: 1, GlobalID: []byte("g")}})
			if !errors.Is(e, ErrCommitUnknown) || !errors.Is(e, ErrXAOutcomeUnknown) || errors.Is(e, driver.ErrBadConn) || !c.closed.Load() {
				t.Fatal(e)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("partial XA left transport open")
			}
			_, writes := fault.Counts()
			if writes != 2 {
				t.Fatal("XA replayed", writes)
			}
		})
	}
}

func TestXARecoveryResponse(t *testing.T) {
	for _, path := range []string{"xa-null", "bad-length", "extra", "leading-ok", "no-rowset", "xa-limit", testXAServerPath} {
		t.Run(path, func(t *testing.T) {
			db := localInfileDB(t, ModeMySQL, false, func(ctx context.Context, p *wire.PacketIO) {
				if !readQuery(ctx, t, p, "XA RECOVER") {
					return
				}
				if path == testXAServerPath {
					if !sendPackets(ctx, t, p, []byte{255, 0x75, 5, '#', 'X', 'A', 'E', '0', '0', 'x'}) || !serveOK(ctx, t, p, []byte{14}, 2) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
					return
				}
				if path == "no-rowset" {
					_ = p.WriteMessage(ctx, commandPacket(0, 0, 2))
					_, _ = p.ReadMessage(ctx)
					return
				}
				if path == "leading-ok" && !sendPackets(ctx, t, p, commandPacket(0, 0, 10)) {
					return
				}
				for _, packet := range [][]byte{{4}, textColumn("f", 3, 63, 0), textColumn("g", 3, 63, 0), textColumn("b", 3, 63, 0), textColumn("d", 252, 63, 0), {254, 0, 0, 2, 0}} {
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
			}, func(cfg *Config) { cfg.Limits.MaxResultItems = 1 })
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, e := db.Conn(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = pinned.Close() }()
			got, e := XARecover(ctx, pinned)
			if e == nil || got != nil {
				t.Fatal(got, e)
			}
			if path == testXAServerPath {
				var xe *XAError
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
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testCompression, testOB20} {
			t.Run(string(mode)+"/"+framing, func(t *testing.T) {
				x := XID{FormatID: 1, GlobalID: []byte("g")}
				db := localInfileDB(t, mode, false, func(ctx context.Context, p *wire.PacketIO) {
					if !p.Framed() {
						t.Error("unframed XA")
						return
					}
					if mode == ModeOracle {
						param := nativeXID(x)
						if !serveOK(ctx, t, p, []byte("\x03SET autocommit=0"), 4) || !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_START(?, ?); END;", []wire.Parameter{{Type: 6}, param, lobInteger(0)}, 0, 4, false) {
							return
						}
						if !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_END(?, ?); END;", []wire.Parameter{{Type: 6}, param, lobInteger(int64(XASuccess))}, 0, 4, false) || !serveOK(ctx, t, p, []byte("\x03SET autocommit=1"), 6) {
							return
						}
						if !serveXAReturn(ctx, t, p, "BEGIN ? := SYS.DBMS_XA.XA_ROLLBACK(?); END;", []wire.Parameter{{Type: 6}, param}, 0, 6, false) {
							return
						}
					} else {
						for _, q := range []string{testXAStartSQL, testXAEndSQL, "XA ROLLBACK X'67',X'',0x1"} {
							if !serveOK(ctx, t, p, []byte("\x03"+q), 2) {
								return
							}
						}
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
				ctx := context.Background()
				pinned, e := db.Conn(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = pinned.Close() }()
				if e = XAStart(ctx, pinned, x, XANoFlags); e != nil {
					t.Fatal(e)
				}
				if e = XAEnd(ctx, pinned, x, XASuccess); e != nil {
					t.Fatal(e)
				}
				if e = XARollback(ctx, pinned, x); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}

func TestXARecover(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "XA RECOVER") || !sendPackets(ctx, t, p, []byte{4}, textColumn("formatID", 3, 63, 0), textColumn("gtrid_length", 3, 63, 0), textColumn("bqual_length", 3, 63, 0), textColumn("data", 252, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{2, '4', '2', 1, '2', 1, '1', 3, 0, 255, 'b'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	got, err := XARecover(context.Background(), pinned)
	if err != nil || len(got) != 1 || got[0].FormatID != 42 || !bytes.Equal(got[0].GlobalID, []byte{0, 255}) || string(got[0].BranchID) != "b" {
		t.Fatal(got, err)
	}
}

func TestXAUnknownCommit(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		_ = readQuery(ctx, t, p, "XA COMMIT X'67',X'',0x1")
	})
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	err = XACommit(context.Background(), pinned, XID{FormatID: 1, GlobalID: []byte("g")}, false)
	if !errors.Is(err, ErrCommitUnknown) || !errors.Is(err, ErrXAOutcomeUnknown) {
		t.Fatal(err)
	}
}
