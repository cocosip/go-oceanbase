package call_test

import (
	bytes "bytes"
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	binary "encoding/binary"
	errors "errors"
	net "net"
	regexp "regexp"
	strings "strings"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	authentication "github.com/cocosip/go-oceanbase/internal/authentication"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	session "github.com/cocosip/go-oceanbase/internal/session"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	testutil "github.com/cocosip/go-oceanbase/internal/testutil"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

const resourceOutEpochQuery = "CALL resource_epoch(?,?)"

func checkResourceOutOrdinaryRow(t *testing.T, rows *sql.Rows) {
	t.Helper()
	var n int64
	if !rows.Next() || rows.Scan(&n) != nil || n != 7 || rows.Next() {
		t.Fatal("ordinary row changed", n, rows.Err())
	}
}

func serveEpochResourceOut(ctx context.Context, t *testing.T, p *wire.PacketIO, typ, ordinary byte, metadata bool, tail byte) bool {
	t.Helper()
	payload, err := wire.EncodePrepareExecute(0, []byte(resourceOutEpochQuery), []wire.Parameter{{Type: 6}, {Type: 6}}, 4096)
	if err != nil {
		t.Error(err)
		return false
	}
	if !testpeer.ReadCommand(ctx, t, p, payload) {
		return false
	}
	flags, count := byte(4), uint16(2)
	if ordinary != 0 {
		flags, count = 0, 1
	}
	if !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(96, count, 2, flags), testpeer.TextColumn("p", 6, 63, 0), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 5, 0}) {
		return false
	}
	if ordinary != 0 {
		column := testpeer.TextColumn("ordinary", ordinary, 45, 0)
		row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 7)
		switch ordinary {
		case 163:
			row = binary.LittleEndian.AppendUint32([]byte{0, 0}, 98)
		case 210, 211:
			row = wire.AppendComplexLenBytes([]byte{0, 0}, publictest.LobOutFixture(3))
		case 160:
			column, row = complexColumnFixture(7), wire.AppendComplexLenBytes([]byte{0, 0, 0}, publictest.LobOutFixture(3))
		}
		if !testpeer.SendPackets(ctx, t, p, column, []byte{254, 0, 0, 5, 0}, row, []byte{254, 0, 0, 5, 0}, publictest.CommandPacket(0, 0, 13), []byte{2}) {
			return false
		}
	}
	status := byte(5)
	if metadata {
		status = 4
	}
	endStatus := status
	if ordinary != 0 {
		endStatus |= 8
	}
	row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 42)
	switch typ {
	case 163:
		row = binary.LittleEndian.AppendUint32(row, 97)
	case 8:
		row = binary.LittleEndian.AppendUint64(row, 9)
	default:
		row = wire.AppendComplexLenBytes(row, publictest.LobOutFixture(3))
	}
	return testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("scalar", 8, 63, 0), testpeer.TextColumn("resource", typ, 45, 0), []byte{254, 0, 0, status, 16}, row, []byte{254, 0, 0, endStatus, 0}, publictest.CommandPacket(0, 0, tail))
}

func complexColumnFixture(version byte) []byte {
	b := testpeer.TextColumn("v", 160, 45, 0)
	return append(b, 1, 'S', 1, 'T', version)
}

const (
	testComplexUnchanged = "OLD"
	testComplexAlias     = "ALIAS"
)

func longDataPacket(mode config.Mode, id uint32, index uint16, piece byte, data []byte) []byte {
	b := binary.LittleEndian.AppendUint16(publictest.StmtPacket(24, id), index)
	if mode == config.ModeOracle {
		b[0] = 162
		b = append(b, piece, 0)
		b = binary.LittleEndian.AppendUint64(b, uint64(len(data)))
	}
	return append(b, data...)
}

type longReaderFunc func([]byte) (int, error)

func (f longReaderFunc) Read(p []byte) (int, error) { return f(p) }

const (
	testOutLength       = "length"
	testOutSelect       = "select"
	testOutCleanup      = "cleanup"
	testOutInit         = "init"
	testOutCall         = "call"
	testOutCloseFailure = "close-failure"
	testOutBefore       = "before"
	testOutVersion      = "4.3.5.1"
)

type failingOutScanner struct{ calls int }

func (s *failingOutScanner) Scan(any) error { s.calls++; return errors.New("scanner rejected output") }

func outputTestConn(t *testing.T, caps uint64, serve func(context.Context, *wire.PacketIO), configure ...func(*config.Config)) publictest.RawConn {
	t.Helper()
	return publictest.OutputTestConn(t, caps, serve, configure...)
}

func outputTestConfig() config.Config {
	return config.Config{Session: config.SessionOptions{Charset: "utf8mb4", Location: time.UTC}, Limits: config.Limits{MaxMessageBytes: 4096, MaxLOBBytes: 4096, MaxResultItems: 16, CleanupTimeout: time.Second}}
}

type cancelOutputCloseConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c cancelOutputCloseConn) Write(data []byte) (int, error) {
	if len(data) == 5 && data[0] == 25 {
		c.cancel()
	}
	return c.Conn.Write(data)
}

type closeFailureConn struct{ net.Conn }

func (c closeFailureConn) Write(data []byte) (int, error) {
	if len(data) == 5 && data[0] == 25 {
		return 0, testutil.ErrInjected
	}
	return c.Conn.Write(data)
}

type callbackOutScanner struct{ scan func() }

func (s callbackOutScanner) Scan(any) error { s.scan(); return nil }

func localInfileDB(t *testing.T, mode config.Mode, capable bool, serve func(context.Context, *wire.PacketIO), configure ...func(*config.Config)) *sql.DB {
	t.Helper()
	cfg := config.Config{User: publictest.TestUser, Mode: mode}
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
			variables = map[string]string{publictest.TestProxyCapability: "256"}
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

const outContextQuery = "CALL p(?,?,?)"

func serveOutContextResponse(ctx context.Context, t *testing.T, p *wire.PacketIO, mode config.Mode) bool {
	t.Helper()
	columns := [][]byte{testpeer.TextColumn("a", 8, 63, 0), testpeer.TextColumn("b", 8, 63, 0), testpeer.TextColumn("c", 8, 63, 0)}
	if mode == config.ModeOracle {
		row := []byte{0, 0}
		for range columns {
			row = binary.LittleEndian.AppendUint64(row, 42)
		}
		return serveResourceOut(ctx, t, p, outContextQuery, columns, row, false, false) && testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 91))
	}
	p.ResetSequence()
	initial, err := p.ReadMessage(ctx)
	pattern := regexp.MustCompile(`^\x03SET (@_obgo_[0-9a-f]{32}_0)=NULL,(@_obgo_[0-9a-f]{32}_1)=NULL,(@_obgo_[0-9a-f]{32}_2)=NULL$`)
	vars := pattern.FindStringSubmatch(string(initial))
	if err != nil || len(vars) != 4 {
		t.Error("unexpected output variable initialization", err)
		return false
	}
	vars = vars[1:]
	if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(0, 0, 2)) || !testpeer.ReadQuery(ctx, t, p, "CALL p( "+strings.Join(vars, " , ")+" )") || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(0, 0, 2)) || !testpeer.ReadQuery(ctx, t, p, "SELECT "+strings.Join(vars, ",")) {
		return false
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{3}, columns[0], columns[1], columns[2], []byte{254, 0, 0, 2, 0}, []byte{2, '4', '2', 2, '4', '2', 2, '4', '2'}, []byte{254, 0, 0, 2, 0}) || !testpeer.ReadQuery(ctx, t, p, "SET "+strings.Join(vars, "=NULL,")+"=NULL") {
		return false
	}
	return testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(0, 0, 2))
}

func serveResourceOut(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, columns [][]byte, row []byte, ordinary, tailError bool) bool {
	t.Helper()
	params := make([]wire.Parameter, len(columns))
	for i := range params {
		params[i].Type = 6
	}
	want, err := wire.EncodePrepareExecute(0, []byte(query), params, 1<<20)
	if err != nil || !testpeer.ReadCommand(ctx, t, p, want) {
		return false
	}
	flags, n := byte(4), uint16(len(columns))
	if ordinary {
		flags, n = 0, 1
	}
	if !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(91, n, uint16(len(columns)), flags)) {
		return false
	}
	for range columns {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("p", 6, 63, 0)) {
			return false
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}) {
		return false
	}
	if ordinary {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("ordinary", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 14, 0, 0, 0}, []byte{byte(len(columns))}) {
			return false
		}
	}
	for _, col := range columns {
		if !testpeer.SendPackets(ctx, t, p, col) {
			return false
		}
	}
	endStatus := byte(6)
	if ordinary {
		endStatus = 14
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 16}, row, []byte{254, 0, 0, endStatus, 0}) {
		return false
	}
	tail := []byte{0, 0, 0, 6, 0, 0, 0}
	if tailError {
		tail = []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}
	}
	return testpeer.SendPackets(ctx, t, p, tail)
}

func outputOperation(t *testing.T) *session.Operation {
	t.Helper()
	physical := session.New(outputTestConfig(), &session.Negotiated{Mode: config.ModeOracle, OBVersion: testOutVersion})
	op, err := physical.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return op
}

func normalizeArgument(t *testing.T, value any) (any, error) {
	t.Helper()
	c := outputTestConn(t, 0, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); testpeer.ServeQuit(t, p) })
	defer func() { _ = c.Close() }()
	named := driver.NamedValue{Ordinal: 1, Value: value}
	err := c.CheckNamedValue(&named)
	return named.Value, err
}
