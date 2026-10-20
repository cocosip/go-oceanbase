package xa_test

import (
	bytes "bytes"
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	strconv "strconv"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	catalog "github.com/cocosip/go-oceanbase/internal/catalog"
	command "github.com/cocosip/go-oceanbase/internal/command"
	session "github.com/cocosip/go-oceanbase/internal/session"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func queryDB(t *testing.T, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	c, err := oceanbase.NewConnector(config.Config{User: testUser, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		serve(context.Background(), p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

func outOracleDB(t *testing.T, prepare config.PrepareMode, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	c, err := oceanbase.NewConnector(config.Config{User: testUser, Mode: config.ModeOracle, PrepareMode: prepare, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if testpeer.ServeConnect(ctx, t, p, 6) && testpeer.ServeBaseline(ctx, t, p, 6) {
			serve(context.Background(), p)
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

//nolint:unparam // Preserve explicit wire fields in the shared protocol fixture.
func localInfileDB(t *testing.T, mode config.Mode, capable bool, serve func(context.Context, *wire.PacketIO), configure ...func(*config.Config)) *sql.DB {
	t.Helper()
	cfg := config.Config{User: testUser, Mode: mode}
	cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		greeting := testpeer.AuthGreeting(authentication.Native, false)
		compressed := cfg.Protocol.Compression == config.FeatureRequired
		ob20 := cfg.Protocol.OB20 == config.FeatureRequired
		if compressed || ob20 {
			greeting = testpeer.ProtocolGreeting(compressed)
		}
		if capable {
			offset := bytes.IndexByte(greeting[1:], 0) + 1 + 1 + 4 + 8 + 1
			greeting[offset] |= 128
		}
		if !testpeer.SendPackets(ctx, t, p, greeting) {
			return
		}
		response, err := p.ReadMessage(ctx)
		if err != nil || len(response) < 4 || (binary.LittleEndian.Uint32(response)&128 != 0) != capable {
			t.Error("local infile capability mismatch", err)
			return
		}
		status := byte(2)
		if mode == config.ModeOracle {
			status = 6
		}
		var variables map[string]string
		if ob20 {
			variables = map[string]string{testProxyCapability: "256"}
		}
		if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(status, variables)) {
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
		if !testpeer.ServeBaseline(ctx, t, p, status) {
			return
		}
		serve(context.Background(), p)
	})
	for _, f := range configure {
		f(&cfg)
	}
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

func combinedHeader(id uint32, columns, params uint16, flags byte) []byte {
	b := binary.LittleEndian.AppendUint32([]byte{0}, id)
	b = binary.LittleEndian.AppendUint16(b, columns)
	b = binary.LittleEndian.AppendUint16(b, params)
	return append(b, 0, 0, 0, flags, 0, 0, 0, 1)
}

func commandPacket(affected, id uint64, status byte) []byte {
	data := binary.LittleEndian.AppendUint64([]byte{254}, affected)
	data = binary.LittleEndian.AppendUint64(append(data, 254), id)
	return append(append([]byte{0}, data...), status, 0, 0, 0)
}

func stmtPacket(cmd byte, id uint32) []byte { return binary.LittleEndian.AppendUint32([]byte{cmd}, id) }

func pinnedSession(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func lobInteger(n int64) wire.Parameter {
	return wire.Parameter{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, uint64(n))}
}

func nativeXID(x oceanbase.XID) wire.Parameter {
	meta := wire.AppendComplexLenBytes(nil, []byte(xaSchema))
	meta = wire.AppendComplexLenBytes(meta, []byte(xaXIDType))
	meta = wire.AppendComplexLenEnc(meta, 0)
	data := wire.AppendComplexLenBytes([]byte{0}, []byte(strconv.FormatInt(int64(x.FormatID), 10)))
	data = wire.AppendComplexLenBytes(data, x.GlobalID)
	data = wire.AppendComplexLenBytes(data, x.BranchID)
	return wire.Parameter{Type: 160, TypeMetadata: meta, Data: data}
}

func serveDescriptorRows(ctx context.Context, t *testing.T, p *wire.PacketIO, kind oceanbase.MetadataKind, schema, name string, rows [][]string) bool {
	t.Helper()
	plan, err := catalog.Plan(config.ModeOracle, "", oceanbase.MetadataFilter{Kind: kind, SchemaPattern: catalog.ExactDescriptorPattern(schema), NamePattern: catalog.ExactDescriptorPattern(name)})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(config.Config{Session: config.SessionOptions{Charset: "utf8mb4"}, Limits: config.Limits{MaxMessageBytes: 1 << 20}}, &session.Negotiated{Mode: config.ModeOracle})
	op, err := s.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	query, err := command.Interpolate(op, plan.SQL, plan.Args)
	if err != nil {
		t.Fatal(err)
	}
	if !testpeer.ReadQuery(ctx, t, p, string(query)) || !testpeer.SendPackets(ctx, t, p, []byte{byte(len(plan.Columns))}) {
		return false
	}
	for _, column := range plan.Columns {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn(column.Name, 253, 45, 0)) {
			return false
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}) {
		return false
	}
	for _, row := range rows {
		var data []byte
		for _, value := range row {
			data = append(data, byte(len(value)))
			data = append(data, value...)
		}
		if !testpeer.SendPackets(ctx, t, p, data) {
			return false
		}
	}
	return testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0})
}

const (
	testUser            = "app"
	testProxyCapability = "ob_capability_flag"
	testStandard        = "standard"
	testCompression     = "compression"
	testOB20            = "ob20"
	xaSchema            = "SYS"
	xaXIDType           = "DBMS_XA_XID"
	xaArrayType         = "DBMS_XA_XID_ARRAY"
)

func TestXAValidationPublicBoundary(t *testing.T) {
	x := oceanbase.XID{FormatID: 7, GlobalID: []byte("g")}
	if err := oceanbase.XAStart(context.Background(), nil, x, oceanbase.XANoFlags); !errors.Is(err, oceanbase.ErrXA) {
		t.Fatal(err)
	}
	if _, err := oceanbase.XARecover(context.Background(), nil); err == nil {
		t.Fatal("nil recovery connection")
	}
}
