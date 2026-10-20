package xa

import (
	context "context"
	driver "database/sql/driver"
	errors "errors"
	io "io"
	net "net"
	reflect "reflect"
	testing "testing"
	time "time"

	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	catalog "github.com/cocosip/go-oceanbase/internal/catalog"
	session "github.com/cocosip/go-oceanbase/internal/session"
	testutil "github.com/cocosip/go-oceanbase/internal/testutil"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
	types "github.com/cocosip/go-oceanbase/types"
)

func TestXAValidation(t *testing.T) {
	x := XID{FormatID: 7, GlobalID: []byte("g")}
	c := &Manager{}
	op := testOperation(t, config.Config{Limits: config.Limits{MaxResultItems: 2}}, &session.Negotiated{Mode: config.ModeMySQL, OK: wire.OK{Status: 2}})
	for _, flags := range []int32{Success, Fail, Join | Resume, -1} {
		if err := c.Validate(op, &Request{Op: Start, XID: x, Flags: flags}, false); !errors.Is(err, dberrors.ErrXA) {
			t.Fatal(flags, err)
		}
	}
	for _, invalid := range []XID{{}, {FormatID: -1, GlobalID: []byte("g")}, {GlobalID: make([]byte, 65)}, {GlobalID: []byte("g"), BranchID: make([]byte, 65)}} {
		if ValidXID(invalid) {
			t.Fatal("accepted invalid XID", invalid)
		}
	}
	copyX := CloneXID(x)
	x.GlobalID[0] = 'x'
	if string(copyX.GlobalID) != "g" {
		t.Fatal("XID alias")
	}
	if mysqlXID(XID{FormatID: -2, GlobalID: []byte{0, 255}}) != "X'00ff',X'',0xfffffffe" {
		t.Fatal("negative format encoding")
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

func TestXAOracleRecoveryShape(t *testing.T) {
	x := types.Object{Schema: xaSchema, Type: xaXIDType, Fields: []types.Attribute{{Name: "FORMATID", Type: valueformat.TypeNameNumber, Value: types.Number("42")}, {Name: "GTRID", Type: valueformat.TypeNameRaw, Value: types.Raw{0, 255}}, {Name: "BQUAL", Type: valueformat.TypeNameRaw, Value: types.Raw("b")}}}
	for _, named := range []bool{false, true} {
		a := types.Array{Schema: xaSchema, Type: xaArrayType, ElementType: xaXIDType, Elements: []any{x}}
		if !named {
			a.Schema, a.Type = "", ""
		}
		v, e := a.Value()
		if e != nil {
			t.Fatal(e)
		}
		got, e := decodeOracleRecovery([]driver.Value{v}, config.Limits{MaxResultItems: 2, MaxMessageBytes: 1024})
		if e != nil || len(got) != 1 || !reflect.DeepEqual(got[0], XID{FormatID: 42, GlobalID: []byte{0, 255}, BranchID: []byte("b")}) {
			t.Fatal(got, e)
		}
	}
	x.Fields[2].Value = nil
	v, e := (types.Array{Schema: xaSchema, Type: xaArrayType, ElementType: xaXIDType, Elements: []any{x}}).Value()
	if e != nil {
		t.Fatal(e)
	}
	got, e := decodeOracleRecovery([]driver.Value{v}, config.Limits{MaxResultItems: 2, MaxMessageBytes: 1024})
	if e != nil || len(got) != 1 || len(got[0].BranchID) != 0 {
		t.Fatal("NULL BQUAL must represent the legal empty branch qualifier", got, e)
	}
}

func TestXARecoveredBranchLimit(t *testing.T) {
	c := &Manager{used: true, branches: map[string]branch{"xa_identity": {stage: prepared}}}
	op := testOperation(t, config.Config{Limits: config.Limits{MaxResultItems: 1}}, &session.Negotiated{Mode: config.ModeMySQL})
	if e := c.Validate(op, &Request{Op: Prepare, XID: XID{FormatID: 1, GlobalID: []byte("g")}}, false); e == nil {
		t.Fatal("unbounded recovered branch registration")
	}
}

func TestXAOracleFlags(t *testing.T) {
	x := XID{GlobalID: []byte("g")}
	c := &Manager{used: true, branches: map[string]branch{xidKey(x): {stage: prepared}}}
	op := testOperation(t, config.Config{Limits: config.Limits{MaxResultItems: 8}}, &session.Negotiated{Mode: config.ModeOracle})
	for _, flags := range []int32{OracleReadOnly, OracleSerializable, OracleLoose} {
		if !validFlags(config.ModeOracle, &Request{Op: Start, Flags: flags}) || validFlags(config.ModeMySQL, &Request{Op: Start, Flags: flags}) {
			t.Fatal(flags)
		}
		if e := c.Validate(op, &Request{Op: Start, XID: x, Flags: flags}, false); !errors.Is(e, dberrors.ErrXA) {
			t.Fatal("prepared branch restarted", flags, e)
		}
	}
	for _, flags := range []int32{Migrate, Suspend | Migrate} {
		if !validFlags(config.ModeOracle, &Request{Op: End, Flags: flags}) || validFlags(config.ModeMySQL, &Request{Op: End, Flags: flags}) {
			t.Fatal(flags)
		}
	}
}

func TestXALocalTransactionGuard(t *testing.T) {
	c := &Manager{}
	op := testOperation(t, config.Config{Limits: config.Limits{MaxResultItems: 4}}, &session.Negotiated{Mode: config.ModeMySQL, OK: wire.OK{Status: 3}})
	if e := c.Validate(op, &Request{Op: Start, XID: XID{GlobalID: []byte("g")}}, false); !errors.Is(e, dberrors.ErrXA) {
		t.Fatal(e)
	}
	op.SetOK(wire.OK{Status: 2})
	if e := c.Validate(op, &Request{Op: Recover}, true); !errors.Is(e, dberrors.ErrXA) {
		t.Fatal(e)
	}
}

func TestXAPartialSend(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(string(mode), func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close(); _ = server.Close() }()
			fault := testutil.NewFaultConn(client, testutil.Faults{MaxWrite: 2, WriteFailAt: 2})
			c := &Manager{}
			op := testOperation(t, outputTestConfig(), &session.Negotiated{Raw: fault, Conn: fault, Packets: wire.NewPacketIO(fault, 4096), Mode: mode, OBVersion: "4.3.5.1", OK: wire.OK{Status: 2}})
			done := make(chan struct{})
			go func() { defer close(done); _, _ = io.Copy(io.Discard, server) }()
			e := c.Execute(context.Background(), op, &catalog.Resolver{}, &Request{Op: Commit, XID: XID{FormatID: 1, GlobalID: []byte("g")}})
			if !errors.Is(e, dberrors.ErrCommitUnknown) || !errors.Is(e, dberrors.ErrXAOutcomeUnknown) || errors.Is(e, driver.ErrBadConn) || !op.Closed() {
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

func outputTestConfig() config.Config {
	return config.Config{Session: config.SessionOptions{Charset: "utf8mb4", Location: time.UTC}, Limits: config.Limits{MaxMessageBytes: 4096, MaxLOBBytes: 4096, MaxResultItems: 16, CleanupTimeout: time.Second}}
}

func testOperation(t *testing.T, cfg config.Config, p *session.Negotiated) *session.Operation {
	t.Helper()
	s := session.New(cfg, p)
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return op
}
