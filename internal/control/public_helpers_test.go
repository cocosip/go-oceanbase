package control_test

import (
	bytes "bytes"
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	binary "encoding/binary"
	errors "errors"
	io "io"
	net "net"
	reflect "reflect"
	strings "strings"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	auth "github.com/cocosip/go-oceanbase/internal/auth"
	authentication "github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/control"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func serveWarningReenable(ctx context.Context, t *testing.T, p *wire.PacketIO, path string) {
	t.Helper()
	switch path {
	case "prepared-server":
		if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, 1)) || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 1, 0}) || !testpeer.ReadCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
	case warningClientPreparePath:
		if !testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 )") || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 1, 0}) {
			return
		}
	case serverErrorPath:
		if !testpeer.ReadQuery(ctx, t, p, warningSQL) || !testpeer.SendPackets(ctx, t, p, []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'x'}) {
			return
		}
	}
	if !serveWarningRows(ctx, t, p, []byte("\x07Warning\x041265\x04text")) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func stmtPacket(cmd byte, id uint32) []byte { return binary.LittleEndian.AppendUint32([]byte{cmd}, id) }

const warningClientPreparePath = "prepared-client"

func servePrepare(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, id uint32, params int, columns [][]byte) bool {
	t.Helper()
	return servePrepareStatus(ctx, t, p, query, id, params, columns, 2)
}

func servePrepareStatus(ctx context.Context, t testing.TB, p *wire.PacketIO, query string, id uint32, params int, columns [][]byte, status byte) bool {
	t.Helper()
	if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, query...)) {
		return false
	}
	b := binary.LittleEndian.AppendUint32([]byte{0}, id)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(columns)))
	b = binary.LittleEndian.AppendUint16(b, uint16(params))
	b = append(b, 0, 0, 0)
	if !testpeer.SendPackets(ctx, t, p, b) {
		return false
	}
	for i := 0; i < params; i++ {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("p", 253, 45, 0)) {
			return false
		}
	}
	if params > 0 && !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) {
		return false
	}
	for _, c := range columns {
		if !testpeer.SendPackets(ctx, t, p, c) {
			return false
		}
	}
	return len(columns) == 0 || testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0})
}

const warningSQL = "UPDATE t SET v=1"

const testBatchSQL = "INSERT INTO t VALUES (?)"

func serveWarningRows(ctx context.Context, t *testing.T, p *wire.PacketIO, rows ...[]byte) bool {
	t.Helper()
	if !testpeer.ReadQuery(ctx, t, p, "SHOW WARNINGS") || !testpeer.SendPackets(ctx, t, p, []byte{3}, testpeer.TextColumn("Level", 253, 45, 0), testpeer.TextColumn("Code", 3, 63, 32), testpeer.TextColumn("Message", 253, 45, 0), []byte{254, 0, 0, 2, 0}) {
		return false
	}
	for _, row := range rows {
		if !testpeer.SendPackets(ctx, t, p, row) {
			return false
		}
	}
	return testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0})
}

const serverErrorPath = "server-error"

func batchExecutePacket(t *testing.T, value int64) []byte {
	t.Helper()
	p, err := valuecodec.EncodeParameter(value, config.ModeMySQL, config.SessionOptions{}, 64)
	if err != nil {
		t.Fatal(err)
	}
	b, err := wire.EncodeStmtExecute(7, []wire.Parameter{p}, 128)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

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

const (
	testProxyCapability = "ob_capability_flag"
	testOB20            = "ob20"
	testCompression     = "compression"
	testStandard        = "standard"
)

const testUser = "app"

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

const (
	testAutocommitOn    = "SET autocommit=1"
	testMySQLReadWrite  = "START TRANSACTION READ WRITE"
	testSerializableSQL = "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
	testAutocommitOff   = "SET autocommit=0"
	testCommitSQL       = "COMMIT"
	testRollbackSQL     = "ROLLBACK"
	testOracleReadWrite = "SET TRANSACTION READ WRITE"
)

type txStep struct {
	query  string
	status byte
}

func serveTxSteps(ctx context.Context, t *testing.T, p *wire.PacketIO, steps []txStep) bool {
	for _, step := range steps {
		if !testpeer.ServeOK(ctx, t, p, append([]byte{3}, step.query...), step.status) {
			return false
		}
	}
	return true
}

const warningRowLimitPath = "row-limit"

const warningLeadingPath = "leading-result"

const warningByteLimitPath = "byte-limit"

func testWarningDetailsResponse(t *testing.T, path string) {
	t.Helper()
	db := localInfileDB(t, config.ModeMySQL, false, func(ctx context.Context, p *wire.PacketIO) {
		serveWarningDetailsResponse(ctx, t, p, path)
	}, func(cfg *config.Config) {
		cfg.Limits.MaxMessageBytes = 128
		if path == warningRowLimitPath {
			cfg.Limits.MaxResultItems = 1
		}
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	got, err := oceanbase.ReadWarnings(ctx, pinned)
	if err == nil || got != nil {
		t.Fatalf("invalid warnings returned partial data=%+v %v", got, err)
	}
	if path == serverErrorPath {
		var server *oceanbase.Error
		if !errors.As(err, &server) || server.Code != 1045 {
			t.Fatal(err)
		}
		if err = pinned.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
	} else {
		if err = pinned.Raw(func(raw any) error {
			if raw.(driver.Validator).IsValid() {
				return errors.New("invalid warning response did not discard session")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err = pinned.PingContext(ctx); err == nil {
			t.Fatal("invalid warning response remained usable")
		}
	}
}

func serveWarningDetailsResponse(ctx context.Context, t *testing.T, p *wire.PacketIO, path string) {
	t.Helper()
	if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+warningSQL), 2) || !testpeer.ReadQuery(ctx, t, p, "SHOW WARNINGS") {
		return
	}
	if path == "ok" {
		if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 2)) {
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("non-rowset warning response was reused")
		}
		return
	}
	if path == serverErrorPath {
		if !testpeer.SendPackets(ctx, t, p, []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'x'}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
		return
	}
	if path == warningLeadingPath && !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 10)) {
		return
	}
	columns := byte(3)
	if path == warningColumnsPath {
		columns = 2
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{columns}) {
		return
	}
	for range columns {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("v", 253, 45, 0)) {
			return
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0}) {
		return
	}
	if path == warningLeadingPath {
		// A correct client can close immediately after metadata; both writes may then fail.
		_ = p.WriteMessage(ctx, []byte("\x07Warning\x041265\x04text"))
		_ = p.WriteMessage(ctx, []byte{254, 0, 0, 2, 0})
		_, _ = p.ReadMessage(ctx)
		return
	}
	row := []byte("\x07Warning\x041265\x04text")
	switch path {
	case "null-field":
		row = []byte("\xfb\x041265\x04text")
	case "code-overflow":
		row = []byte("\x07Warning\x0a4294967296\x04text")
	case warningByteLimitPath:
		row = append([]byte("\x07Warning\x041265\x5a"), strings.Repeat("x", 90)...)
	}
	if path != warningColumnsPath && !testpeer.SendPackets(ctx, t, p, row) {
		return
	}
	if (path == warningRowLimitPath || path == warningByteLimitPath) && !testpeer.SendPackets(ctx, t, p, row) {
		return
	}
	if path == "extra-warning-result" && !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 10, 0}) {
		return
	}
	if _, err := p.ReadMessage(ctx); err == nil {
		t.Error("invalid warning response was reused")
	}
}

//nolint:unparam // Preserve explicit wire fields in the shared protocol fixture.
func commandPacket(affected, id uint64, status byte) []byte {
	data := binary.LittleEndian.AppendUint64([]byte{254}, affected)
	data = binary.LittleEndian.AppendUint64(append(data, 254), id)
	return append(append([]byte{0}, data...), status, 0, 0, 0)
}

const warningColumnsPath = "warning-columns"

func sessionScalar(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, status byte, values ...string) bool {
	return sessionScalarTyped(ctx, t, p, query, status, 253, values...)
}

func sessionScalarTyped(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, status, typ byte, values ...string) bool {
	t.Helper()
	if !testpeer.ReadQuery(ctx, t, p, query) || !testpeer.SendPackets(ctx, t, p, []byte{byte(len(values))}) {
		return false
	}
	for range values {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("v", typ, 45, 0)) {
			return false
		}
	}
	row := []byte{}
	for _, v := range values {
		row = append(row, byte(len(v)))
		row = append(row, v...)
	}
	return testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}, row, []byte{254, 0, 0, status, 0})
}

func txDB(t *testing.T, mode config.Mode, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	status := byte(2)
	if mode == config.ModeOracle {
		status = 6
	}
	connector, err := oceanbase.NewConnector(config.Config{User: testUser, Mode: mode, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, status) || !testpeer.ServeBaseline(ctx, t, p, status) {
			return
		}
		serve(context.Background(), p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(connector)
}

const (
	transactionReadOnlyWrite  = "UPDATE readonly_target SET value=1"
	transactionPendingWrite   = "UPDATE pending SET value=1"
	transactionMySQLReadOnly  = "START TRANSACTION READ ONLY"
	transactionOracleReadOnly = "SET TRANSACTION READ ONLY"
	transactionUTF8Names      = "SET NAMES utf8mb4"
	transactionUTCZone        = "SET SESSION time_zone = '+00:00'"
	transactionMySQLLabel     = "mysql"
	transactionOracleLabel    = "oracle"
)

func testCommitContextAfterWrite(t *testing.T, deadline bool) {
	t.Helper()
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(map[config.Mode]string{config.ModeMySQL: transactionMySQLLabel, config.ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			}
			defer cancel()
			afterWrite := cancel
			want := context.Canceled
			if deadline {
				afterWrite = func() { <-ctx.Done() }
				want = context.DeadlineExceeded
			}
			var dials, commits atomic.Int32
			closed := make(chan struct{})
			status := byte(2)
			start := []txStep{{testMySQLReadWrite, 3}}
			if mode == config.ModeOracle {
				status = 6
				start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
			}
			dial := testpeer.PeerDial(t, func(peerctx context.Context, p *wire.PacketIO) {
				defer close(closed)
				if !testpeer.ServeConnect(peerctx, t, p, status) || !testpeer.ServeBaseline(peerctx, t, p, status) {
					return
				}
				peerctx = context.Background()
				if !serveTxSteps(peerctx, t, p, start) || !testpeer.ReadQuery(peerctx, t, p, testCommitSQL) {
					return
				}
				commits.Add(1)
				p.ResetSequence()
				if _, err := p.ReadMessage(context.Background()); err == nil {
					t.Error("unconfirmed commit retained its connection or sent another command")
				}
			})
			connector, err := oceanbase.NewConnector(config.Config{User: testUser, Mode: mode, DialContext: func(dialctx context.Context, network, addr string) (net.Conn, error) {
				dials.Add(1)
				raw, err := dial(dialctx, network, addr)
				if err != nil {
					return raw, err
				}
				return commitBoundaryConn{Conn: raw, afterWrite: afterWrite}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = tx.Commit()
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("unconfirmed commit did not close its physical connection")
			}
			if !errors.Is(err, oceanbase.ErrCommitUnknown) || !errors.Is(err, want) || errors.Is(err, driver.ErrBadConn) {
				t.Fatalf("sent COMMIT cancellation lost outcome uncertainty: %v", err)
			}
			if commits.Load() != 1 || dials.Load() != 1 || db.Stats().OpenConnections != 0 {
				t.Fatalf("commits=%d dials=%d stats=%+v", commits.Load(), dials.Load(), db.Stats())
			}
			if err = tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("unknown commit allowed rollback: %v", err)
			}
		})
	}
}

type commitBoundaryConn struct {
	net.Conn
	afterWrite func()
}

func (c commitBoundaryConn) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	if err == nil && n == len(data) && bytes.Equal(data, []byte("\x03COMMIT")) {
		c.afterWrite()
	}
	return n, err
}

const routeFirst = "first.example:2881"

const routeDialOperation = "connect"

const routeSecond = "second.example:2881"

func assertReadWriteRejected(t *testing.T, pinned *sql.Conn) {
	t.Helper()
	tx, err := pinned.BeginTx(context.Background(), nil)
	if tx != nil {
		_ = tx.Rollback()
	}
	if !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatalf("read-write transaction accepted on read-only service: %v", err)
	}
}

func optimizationExecute(id uint32, value int64, reuse bool) []byte {
	b := append(executeHeader(id), 0, 1)
	if reuse {
		b[len(b)-1] = 0
	} else {
		b = append(b, 8, 0)
	}
	return binary.LittleEndian.AppendUint64(b, uint64(value))
}

func executeHeader(id uint32) []byte { return append(stmtPacket(23, id), 0, 1, 0, 0, 0) }

func optimizationDB(t *testing.T, mode config.Mode, options config.ExecutionOptions, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	status := byte(2)
	if mode == config.ModeOracle {
		status = 6
	}
	connector, err := oceanbase.NewConnector(config.Config{User: testUser, Mode: mode, Execution: options, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, status) || !testpeer.ServeBaseline(ctx, t, p, status) {
			return
		}
		serve(context.Background(), p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	return db
}

func pinnedSession(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

const testReadSessionVariable = "SELECT @@SESSION.wait_timeout"

const testSessionVariable = "wait_timeout"

const testMaxUint64Text = "18446744073709551615"

const (
	sessionReadSchema      = "schema"
	sessionReadCatalog     = "catalog"
	sessionReadInfo        = "info"
	sessionReadVariable    = "variable"
	sessionReadDatabaseSQL = "SELECT DATABASE()"
	sessionReadTailError   = "error-after-row"
	sessionReadExtraRow    = "extra-row"
)

func testSessionReadResponse(t *testing.T, mode config.Mode, framing, path, helper string) {
	t.Helper()
	query, columns := sessionReadFixture(mode, helper)
	status := byte(2)
	if mode == config.ModeOracle {
		status = 6
	}
	db := localInfileDB(t, mode, false, func(ctx context.Context, p *wire.PacketIO) {
		serveSessionReadFailure(ctx, t, p, query, columns, status, path)
	}, func(cfg *config.Config) {
		switch framing {
		case testCompression:
			cfg.Protocol.Compression = config.FeatureRequired
		case testOB20:
			cfg.Protocol.OB20 = config.FeatureRequired
		}
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	value, err := readSessionFixture(ctx, pinned, helper)
	if !reflect.ValueOf(value).IsZero() {
		t.Error("failed scalar read published a partial value", value)
	}
	if path == "no-row" || path == sessionReadExtraRow {
		if !errors.Is(err, wire.ErrMalformed) {
			t.Fatal("invalid scalar shape was accepted", err)
		}
		if err = pinned.PingContext(ctx); err == nil {
			t.Fatal("malformed scalar response remained usable")
		}
		return
	}
	var server *oceanbase.Error
	if !errors.As(err, &server) || server.Code != 1045 || server.SQLState != "42000" || server.Message != "session query denied" {
		t.Fatal("complete server error lost its cause", err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal("complete scalar error discarded original session", err)
	}
}

func sessionReadFixture(mode config.Mode, helper string) (string, int) {
	query, columns := sessionReadDatabaseSQL, 1
	switch helper {
	case sessionReadSchema:
		if mode == config.ModeOracle {
			query = "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')"
		}
	case sessionReadInfo:
		query, columns = "SELECT @ApplicationName, @ClientUser, @ClientHostname", 3
	case sessionReadVariable:
		query = testReadSessionVariable
	}
	if mode == config.ModeOracle {
		query += testDualSuffix
	}
	return query, columns
}

func serveSessionReadFailure(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, columns int, status byte, path string) {
	t.Helper()
	if !testpeer.ReadQuery(ctx, t, p, query) || !testpeer.SendPackets(ctx, t, p, []byte{byte(columns)}) {
		return
	}
	for range columns {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("v", 253, 45, 0)) {
			return
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) {
		return
	}
	row := make([]byte, 0, columns*2)
	for range columns {
		row = append(row, 1, 'v')
	}
	if path == sessionReadTailError || path == sessionReadExtraRow {
		if !testpeer.SendPackets(ctx, t, p, row) {
			return
		}
	}
	switch path {
	case "error-before-row", sessionReadTailError:
		packet := []byte{255, 21, 4, '#', '4', '2', '0', '0', '0'}
		if !testpeer.SendPackets(ctx, t, p, append(packet, "session query denied"...)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	default:
		packet := []byte{254, 0, 0, status, 0}
		if path == sessionReadExtraRow {
			packet = row
		}
		if !testpeer.SendPackets(ctx, t, p, packet) {
			return
		}
		_, err := p.ReadMessage(ctx)
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Error("malformed scalar response did not close the connection", err)
		}
	}
}

func readSessionFixture(ctx context.Context, pinned *sql.Conn, helper string) (any, error) {
	switch helper {
	case sessionReadSchema:
		return oceanbase.CurrentSchema(ctx, pinned)
	case sessionReadCatalog:
		return oceanbase.CurrentCatalog(ctx, pinned)
	case sessionReadInfo:
		return oceanbase.GetClientInfo(ctx, pinned)
	default:
		return oceanbase.SessionVariable(ctx, pinned, "wait_timeout")
	}
}

func testSavepointTxContext(t *testing.T, mode config.Mode, framing string, operation control.SavepointKind, deadline bool) {
	t.Helper()
	txctx, cancelTx := context.WithCancel(t.Context())
	if deadline {
		cancelTx()
		txctx, cancelTx = context.WithTimeout(t.Context(), time.Second)
	}
	defer cancelTx()
	start := []txStep{{testMySQLReadWrite, 3}}
	active := byte(3)
	quoted := "`pending`"
	if mode == config.ModeOracle {
		start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
		active, quoted = 5, "\"pending\""
	}
	verb := "SAVEPOINT "
	switch operation {
	case control.RollbackSavepoint:
		verb = "ROLLBACK TO SAVEPOINT "
	case control.ReleaseSavepoint:
		verb = "RELEASE SAVEPOINT "
	}
	var sessions atomic.Int32
	closed := make(chan struct{})
	db := batchRewriteDB(t, config.Config{Mode: mode}, framing, func(ctx context.Context, p *wire.PacketIO) {
		if sessions.Add(1) != 1 {
			t.Error("canceled savepoint opened a second physical connection")
			return
		}
		defer close(closed)
		if !serveTxSteps(ctx, t, p, start) || !serveTxSteps(ctx, t, p, []txStep{{"SAVEPOINT " + quoted, active}}) || !testpeer.ReadQuery(ctx, t, p, verb+quoted) {
			return
		}
		if !deadline {
			cancelTx()
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("canceled savepoint sent cleanup SQL or reused an unconfirmed response")
		}
	})
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(txctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = oceanbase.Savepoint(txctx, tx, "pending"); err != nil {
		t.Fatal(err)
	}
	opctx, cancelOp := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelOp()
	switch operation {
	case control.CreateSavepoint:
		err = oceanbase.Savepoint(opctx, tx, "pending")
	case control.RollbackSavepoint:
		err = oceanbase.RollbackTo(opctx, tx, "pending")
	case control.ReleaseSavepoint:
		err = oceanbase.ReleaseSavepoint(opctx, tx, "pending")
	}
	if opctx.Err() != nil || !errors.Is(err, txctx.Err()) || errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("savepoint ignored its transaction context: operation=%v transaction=%v error=%v", opctx.Err(), txctx.Err(), err)
	}
	select {
	case <-closed:
	case <-opctx.Done():
		t.Fatal("canceled savepoint did not close its physical connection")
	}
	if sessions.Load() != 1 {
		t.Fatal("canceled savepoint was replayed")
	}
	if err = tx.Commit(); !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("canceled transaction allowed commit", err)
	}
}

func batchRewriteDB(t *testing.T, cfg config.Config, variant string, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	status := byte(2)
	if cfg.Mode == config.ModeOracle {
		status = 6
	}
	cfg.User = testUser
	cfg.Protocol = config.ProtocolOptions{Compression: config.FeatureOff, OB20: config.FeatureOff}
	switch variant {
	case testCompression:
		cfg.Protocol.Compression = config.FeatureRequired
	case testOB20:
		cfg.Protocol.OB20 = config.FeatureRequired
	}
	cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		greeting := testpeer.ProtocolGreeting(variant == testCompression)
		// The high capability word starts after the low word, charset and status.
		start := 1
		for greeting[start] != 0 {
			start++
		}
		start += 1 + 4 + 8 + 1 + 2 + 1 + 2
		if variant != "no-offer" {
			binary.LittleEndian.PutUint16(greeting[start:], binary.LittleEndian.Uint16(greeting[start:])|uint16(wire.ClientMultiStatements>>16))
		}
		if !testpeer.SendPackets(ctx, t, p, greeting) {
			return
		}
		if _, err := p.ReadMessage(ctx); err != nil {
			t.Error(err)
			return
		}
		vars := map[string]string{}
		if variant == testOB20 {
			vars[testProxyCapability] = "33024"
		}
		if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(status, vars)) {
			return
		}
		switch variant {
		case testCompression:
			if err := p.EnableCompression(); err != nil {
				t.Error(err)
				return
			}
		case testOB20:
			if err := p.EnableOB20(wire.OB20Options{ConnectionID: 42, Checksum: true, NewExtra: true}); err != nil {
				t.Error(err)
				return
			}
		}
		ready := false
		if cfg.Session.Charset == localFilenameCharset {
			ready = testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES gbk"), status) && testpeer.ServeOK(ctx, t, p, []byte("\x03"+testAutocommitOn), status) && testpeer.ServeVersion(ctx, t, p, status, false) && testpeer.ServeOK(ctx, t, p, []byte("\x03"+transactionUTCZone), status)
		} else {
			ready = testpeer.ServeBaseline(ctx, t, p, status)
		}
		if ready && cfg.Session.NCharset != "" {
			ready = testpeer.ServeOK(ctx, t, p, []byte("\x03SET NCHARACTER_SET_CONNECTION = '"+cfg.Session.NCharset+"'"), status)
		}
		if ready {
			serve(context.Background(), p)
		}
	})
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

const localFilenameCharset = "gbk"

func testSavepointCharset(t *testing.T, mode config.Mode, encoding, framing string) {
	t.Helper()
	start := []txStep{{testMySQLReadWrite, 3}}
	end := []txStep{{testRollbackSQL, 2}}
	active, ready := byte(3), byte(2)
	quoted := "`中\"``;x`"
	if encoding == localFilenameCharset {
		quoted = "`\xd6\xd0\"``;x`"
	}
	if mode == config.ModeOracle {
		start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
		end = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}
		active, ready = 5, 6
		quoted = "\"中\"\"`;x\""
		if encoding == localFilenameCharset {
			quoted = "\"\xd6\xd0\"\"`;x\""
		}
	}
	var sessions atomic.Int32
	done := make(chan struct{})
	db := batchRewriteDB(t, config.Config{Mode: mode, Session: config.SessionOptions{Charset: encoding}}, framing, func(ctx context.Context, p *wire.PacketIO) {
		if sessions.Add(1) != 1 {
			t.Error("savepoint opened a second physical connection")
			return
		}
		defer close(done)
		if !serveTxSteps(ctx, t, p, start) {
			return
		}
		steps := []txStep{{"SAVEPOINT " + quoted, active}, {"ROLLBACK TO SAVEPOINT " + quoted, active}}
		if mode == config.ModeMySQL {
			steps = append(steps, txStep{"RELEASE SAVEPOINT " + quoted, active})
		}
		if !serveTxSteps(ctx, t, p, steps) || !serveTxSteps(ctx, t, p, end) || !testpeer.ServeOK(ctx, t, p, []byte{14}, ready) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	name := "中\"`;x"
	if err = oceanbase.Savepoint(ctx, tx, name); err != nil {
		t.Fatal(err)
	}
	if err = oceanbase.RollbackTo(ctx, tx, name); err != nil {
		t.Fatal(err)
	}
	err = oceanbase.ReleaseSavepoint(ctx, tx, name)
	if mode == config.ModeOracle {
		if !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal("Oracle RELEASE did not reject before sending", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("savepoint changed the session boundary", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if sessions.Load() != 1 || db.Stats().OpenConnections != 0 {
		t.Fatal("savepoint lost its physical connection ownership")
	}
}

const testUTF8MB4 = "utf8mb4"

const selectBindSQL = "SELECT ?"

const testCollationSQL = "SET NAMES utf8mb4 COLLATE utf8mb4_bin"

func serveCollationBaseline(ctx context.Context, t *testing.T, p *wire.PacketIO, names string) bool {
	t.Helper()
	return testpeer.ServeOK(ctx, t, p, []byte("\x03"+names), 2) && testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=1"), 2) && testpeer.ServeVersion(ctx, t, p, 2, false) && testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) && testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES latin1"), 2) && testpeer.ServeOK(ctx, t, p, []byte("\x03"+names), 2) && testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2)
}

const testCollationUTF8 = "utf8mb4_bin"

const testDriverDialectValue = "__ob_jdbc_client"

const changeUserName = "next@tenant"

const testDriverDialectAttribute = "__mysql_client_type"

const testClientAttributeCapability = "__ob_client_attribute_capability_flag"

const testAttributeGBKValue = "\xb2\xe2\xca\xd4"

const testCollationGBK = "gbk_bin"

const changeUserPassword = "next-secret"

const testProxyRequestCapability = "__proxy_capability_flag"

const testCollationLatin1 = "latin1_bin"

func changeUserPacket(t *testing.T, mode config.Mode, database string) []byte {
	t.Helper()
	response, err := auth.Native(changeUserPassword, []byte("12345678901234567890"))
	if err != nil {
		t.Fatal(err)
	}
	if mode == config.ModeOracle && database == "" {
		database = "next"
	}
	b := append([]byte{17}, changeUserName...)
	b = append(b, 0, byte(len(response)))
	b = append(b, response...)
	b = append(b, database...)
	b = append(b, 0)
	b = append(b, authentication.Native...)
	return append(b, 0)
}

const testCustomPlugin = "custom"

type lifecycleAuth struct {
	closed   *atomic.Int32
	fail     error
	closeErr error
	security config.AuthSecurity
}

func (p *lifecycleAuth) Close() error { p.closed.Add(1); return p.closeErr }

func (p *lifecycleAuth) Respond(context.Context, config.Credentials, config.AuthChallenge) ([]byte, error) {
	return []byte("response"), p.fail
}

func (p *lifecycleAuth) Security(config.AuthChallenge) config.AuthSecurity { return p.security }

const authTLSDeniedPath = "tls-denied"

const changeUserModeChange = "mode-change"

func serveCancelFailure(ctx context.Context, t *testing.T, p *wire.PacketIO, name string) {
	t.Helper()
	if !testpeer.ReadQuery(ctx, t, p, "KILL QUERY 42") {
		return
	}
	switch name {
	case "lost-ack":
		return
	case cancelPermissionPath:
		if !testpeer.SendPackets(ctx, t, p, []byte{255, 203, 4, '#', '4', '2', '0', '0', '0', 'x'}) {
			return
		}
	case "bad-kill-response":
		if !testpeer.SendPackets(ctx, t, p, []byte{0}) {
			return
		}
	case "extra-results":
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 10, 0, 0, 0}) {
			return
		}
	}
	p.ResetSequence()
	if name == cancelPermissionPath {
		testpeer.ServeQuit(t, p)
	} else if _, e := p.ReadMessage(ctx); e == nil {
		t.Error("malformed helper stayed open")
	}
}

const cancelPermissionPath = "permission"

func serveCancelConnect(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte, id uint32) bool {
	t.Helper()
	greeting := testpeer.AuthGreeting(authentication.Native, false)
	binary.LittleEndian.PutUint32(greeting[bytes.IndexByte(greeting, 0)+1:], id)
	return testpeer.SendPackets(ctx, t, p, greeting) && testpeer.ReadAuthResponse(ctx, t, p) && testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, status, 0, 0, 0})
}

func testCancelQueryIdentity(t *testing.T, status byte, createDatabase, skipVersion bool) {
	t.Helper()
	started, killed := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var dials atomic.Int32
	cfg := config.Config{User: testUser, Session: config.SessionOptions{SkipVersionProbe: skipVersion}, DialContext: testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
		aux := dials.Add(1) > 1
		id := uint32(42)
		if aux {
			id++
		}
		if !serveCancelConnect(ctx, t, p, status, id) || !serveVersionProbeBaseline(ctx, t, p, status, skipVersion) {
			return
		}
		if !aux && createDatabase {
			if !testpeer.ServeOK(ctx, t, p, []byte(testCreateDatabaseQuery), status) || !testpeer.ServeOK(ctx, t, p, []byte(testCreateDatabaseSelect), status) {
				return
			}
		}
		if aux {
			if !testpeer.ServeOK(ctx, t, p, []byte("\x03KILL QUERY 42"), status) {
				return
			}
			close(killed)
		} else {
			if !testpeer.ReadQuery(ctx, t, p, "SELECT waiting") {
				return
			}
			close(started)
			select {
			case <-killed:
			case <-ctx.Done():
				return
			}
			if !testpeer.SendPackets(ctx, t, p, []byte{255, 37, 5, '#', '7', '0', '1', '0', '0', 'x'}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})}
	if createDatabase {
		cfg.Database, cfg.CreateDatabaseIfMissing = testCreateDatabaseName, true
	}
	configured, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(configured)
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	target, err := oceanbase.QueryTarget(ctx, pinned)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := pinned.ExecContext(ctx, "SELECT waiting"); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = oceanbase.CancelQuery(ctx, configured, target); err != nil {
		t.Fatal(err)
	}
	var server *oceanbase.Error
	if err = <-done; !errors.As(err, &server) || server.Code != 1317 {
		t.Fatalf("original query result=%v", err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = oceanbase.CancelQuery(ctx, configured, target); !errors.Is(err, oceanbase.ErrResource) || dials.Load() != 2 {
		t.Fatalf("idle target dialed auxiliary=%d error=%v", dials.Load(), err)
	}
}

func serveVersionProbeBaseline(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte, skip bool) bool {
	t.Helper()
	if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=1"), status) {
		return false
	}
	if !skip && !testpeer.ServeVersion(ctx, t, p, status, false) {
		return false
	}
	return testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status)
}

const (
	testCreateDatabaseMode    = "oracle-mode"
	testCreateDatabaseTimeout = "timeout"
	testCreateDatabaseName    = "example"
	testCreateDatabaseQuery   = "\x03CREATE DATABASE IF NOT EXISTS `example`"
	testCreateDatabaseSelect  = "\x02example"
)

const testDualSuffix = " FROM dual"
