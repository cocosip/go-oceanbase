package publictest

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	strings "strings"
	atomic "sync/atomic"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

// CapabilityDB negotiates an explicit protocol capability for a public API scenario.
func CapabilityDB(t *testing.T, cfg config.Config, caps uint64, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	return sql.OpenDB(configuredConnector(t, cfg, "4.3.5.1", caps, serve))
}

// LobLengthPrefix is a shared public API protocol fixture.
func LobLengthPrefix(n int) []byte {
	if n < 251 {
		return []byte{byte(n)}
	}
	if n <= 65535 {
		return []byte{252, byte(n), byte(n >> 8)}
	}
	if n <= 0xffffff {
		return []byte{253, byte(n), byte(n >> 8), byte(n >> 16)}
	}
	return binary.LittleEndian.AppendUint64([]byte{254}, uint64(n))
}

// TableDictionary is a shared public API protocol fixture.
func TableDictionary() [][]any {
	return [][]any{{"s", "t", "BASE TABLE", "InnoDB", "id", "bigint", "", "1"}, {"s", "t", "BASE TABLE", "InnoDB", "b", "longblob", "", nil}}
}

// Resource protocol fixture values.
const (
	TestNonLockingEngine = "MyISAM"
	TestBlobRead         = "read"
	TestBecameNull       = "became-null"
	TestSQLNull          = "null-value"
	TestUnknownEngine    = "unknown-engine"
	TestOctetLength      = "OCTET_LENGTH"
	TestForUpdate        = "FOR UPDATE"
)

// ReadTableCommand is a shared public API protocol fixture.
func ReadTableCommand(ctx context.Context, t testing.TB, p *wire.PacketIO) []byte {
	t.Helper()
	p.ResetSequence()
	b, err := p.ReadMessage(ctx)
	if err != nil {
		t.Error(err)
		return nil
	}
	return b
}

// TableTarget is a shared public API protocol fixture.
func TableTarget() oceanbase.BlobTarget {
	return oceanbase.BlobTarget{Schema: "s", Table: "t", Column: "b", Key: map[string]any{"id": int64(7)}}
}

// Resource protocol fixture values.
const (
	TestAutocommitOn    = "SET autocommit=1"
	TestMySQLReadWrite  = "START TRANSACTION READ WRITE"
	TestSerializableSQL = "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
	TestAutocommitOff   = "SET autocommit=0"
	TestCommitSQL       = "COMMIT"
	TestRollbackSQL     = "ROLLBACK"
	TestOracleReadWrite = "SET TRANSACTION READ WRITE"
)

// CombinedQueryPacket is a shared public API protocol fixture.
func CombinedQueryPacket(id uint32, query string, value int64, reuse, _ bool) []byte {
	b := append(StmtPacket(161, id), 0, 1, 0, 0, 0, byte(len(query)))
	b = append(b, query...)
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = append(b, 0, 1)
	if reuse {
		b[len(b)-1] = 0
	} else {
		b = append(b, 8, 0)
	}
	b = binary.LittleEndian.AppendUint64(b, uint64(value))
	b = append(b, make([]byte, 16)...)
	var sum uint32
	for _, v := range []byte(query) {
		sum ^= uint32(v)
		for range 8 {
			if sum&1 != 0 {
				sum = sum>>1 ^ 0x82f63b78
			} else {
				sum >>= 1
			}
		}
	}
	binary.LittleEndian.PutUint32(b[len(b)-8:], sum)
	return b
}

// StmtPacket is a shared public API protocol fixture.
func StmtPacket(cmd byte, id uint32) []byte { return binary.LittleEndian.AppendUint32([]byte{cmd}, id) }

// Resource protocol fixture values.
const (
	TestProxyCapability = "ob_capability_flag"
	TestOB20            = "ob20"
	TestCompression     = "compression"
	TestStandard        = "standard"
)

// Resource protocol fixture values.
const (
	TransactionReadOnlyWrite  = "UPDATE readonly_target SET value=1"
	TransactionPendingWrite   = "UPDATE pending SET value=1"
	TransactionMySQLReadOnly  = "START TRANSACTION READ ONLY"
	TransactionOracleReadOnly = "SET TRANSACTION READ ONLY"
	TransactionUTF8Names      = "SET NAMES utf8mb4"
	TransactionUTCZone        = "SET SESSION time_zone = '+00:00'"
	TransactionMySQLLabel     = "mysql"
	TransactionOracleLabel    = "oracle"
)

// ExecContext records an unexpected call through an unsupported session wrapper.
func (s *ResourceSessionWrapper) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	s.Calls.Add(1)
	return nil, errors.New("unexpected session call")
}

// ResourceSessionWrapper contains a shared protocol fixture value.
type ResourceSessionWrapper struct {
	oceanbase.Session
	Calls atomic.Int32
}

// TestLOBABC is the canonical small BLOB fixture.
const TestLOBABC = "abc"

// CombinedHeader is a shared public API protocol fixture.
func CombinedHeader(id uint32, columns, params uint16, flags byte) []byte {
	b := binary.LittleEndian.AppendUint32([]byte{0}, id)
	b = binary.LittleEndian.AppendUint16(b, columns)
	b = binary.LittleEndian.AppendUint16(b, params)
	return append(b, 0, 0, 0, flags, 0, 0, 0, 1)
}

// TableBlobEpochPingSQL checks the session after a resource epoch change.
const TableBlobEpochPingSQL = "SELECT 1"

// CursorFetchFixture is a shared public API protocol fixture.
func CursorFetchFixture(id, count uint32) []byte {
	return binary.LittleEndian.AppendUint32(StmtPacket(28, id), count)
}

// LobInlineFixture is a shared public API protocol fixture.
func LobInlineFixture(value string) []byte {
	return append([]byte{'!', 'B', 'O', 'L', 2, 0xa0, 0, 0}, value...)
}

// LobIntFixture is a shared public API protocol fixture.
func LobIntFixture(n uint64) wire.Parameter {
	return wire.Parameter{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, n)}
}

// TestUser is the user accepted by local protocol fixtures.
const TestUser = "app"

// ExecuteHeader is a shared public API protocol fixture.
func ExecuteHeader(id uint32) []byte { return append(StmtPacket(23, id), 0, 1, 0, 0, 0) }

// PinnedSession is a shared public API protocol fixture.
func PinnedSession(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TxStep contains a shared protocol fixture value.
type TxStep struct {
	Query  string
	Status byte
}

// OutOracleDB is a shared public API protocol fixture.
func OutOracleDB(t *testing.T, prepare config.PrepareMode, serve func(context.Context, *wire.PacketIO), configure ...func(*config.Config)) *sql.DB {
	t.Helper()
	cfg := config.Config{User: TestUser, Mode: config.ModeOracle, PrepareMode: prepare, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if testpeer.ServeConnect(ctx, t, p, 6) && testpeer.ServeBaseline(ctx, t, p, 6) {
			serve(context.Background(), p)
		}
	})}
	for _, apply := range configure {
		apply(&cfg)
	}
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

// QueryDB is a shared public API protocol fixture.
func QueryDB(t *testing.T, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	c, err := oceanbase.NewConnector(config.Config{User: TestUser, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
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

// LobOutFixture is a shared public API protocol fixture.
func LobOutFixture(length uint64) []byte {
	data := make([]byte, 36)
	copy(data, []byte{'!', 'B', 'O', 'L', 2})
	data[9] = 1
	binary.LittleEndian.PutUint64(data[28:], length)
	return data
}

// Resource protocol fixture values.
const (
	LobOpenEpochQuery   = "SELECT lob FROM lob_open_epoch WHERE id=?"
	LobOpenEpochLiteral = "SELECT lob FROM lob_open_epoch WHERE id= 1 "
	LobOpenText         = "text"
	LobOpenClient       = "client"
	LobOpenServer       = "server"
	LobOpenCombined     = "combined"
	LobOpenPrepare      = "prepare"
	LobOpenMetadata     = "metadata"
	LobOpenFinal        = "final"
)

// ServeLOBCallStatus is a shared public API protocol fixture.
func ServeLOBCallStatus(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, params []wire.Parameter, columns [][]byte, row []byte, status byte) bool {
	t.Helper()
	command, err := wire.EncodePrepareExecute(0, []byte(query), params, 64<<20)
	if err != nil || !testpeer.ReadCommand(ctx, t, p, command) {
		return false
	}
	packets := [][]byte{CombinedHeader(41, uint16(len(columns)), uint16(len(params)), 4)}
	for _, param := range params {
		packets = append(packets, testpeer.TextColumn("p", byte(param.Type), 63, 0))
	}
	packets = append(packets, []byte{254, 0, 0, status, 0})
	packets = append(packets, columns...)
	packets = append(packets, []byte{254, 0, 0, status, 16}, row, []byte{254, 0, 0, status, 0}, []byte{0, 0, 0, status, 0, 0, 0})
	return testpeer.SendPackets(ctx, t, p, packets...) && testpeer.ReadCommand(ctx, t, p, StmtPacket(25, 41))
}

// ServeLOBCall is a shared public API protocol fixture.
func ServeLOBCall(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, params []wire.Parameter, columns [][]byte, row []byte) bool {
	return ServeLOBCallStatus(ctx, t, p, query, params, columns, row, 6)
}

// ServePrepare is a shared public API protocol fixture.
func ServePrepare(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, id uint32, params int, columns [][]byte) bool {
	t.Helper()
	return ServePrepareStatus(ctx, t, p, query, id, params, columns, 2)
}

// ServePrepareStatus is a shared public API protocol fixture.
func ServePrepareStatus(ctx context.Context, t testing.TB, p *wire.PacketIO, query string, id uint32, params int, columns [][]byte, status byte) bool {
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

// ServeTableDictionary is a shared public API protocol fixture.
func ServeTableDictionary(ctx context.Context, t testing.TB, p *wire.PacketIO, status byte, rows ...[]any) bool {
	return ServeTableRows(ctx, t, p, status, []string{"information_schema.TABLES", "information_schema.COLUMNS", "information_schema.KEY_COLUMN_USAGE"}, []byte{253, 253, 253, 253, 253, 253, 253, 253}, rows...)
}

// ServeTableRows is a shared public API protocol fixture.
func ServeTableRows(ctx context.Context, t testing.TB, p *wire.PacketIO, status byte, contains []string, types []byte, rows ...[]any) bool {
	t.Helper()
	command := ReadTableCommand(ctx, t, p)
	if len(command) == 0 || command[0] != 3 {
		t.Errorf("expected table SQL, got %x", command)
		return false
	}
	for _, part := range contains {
		if !strings.Contains(string(command[1:]), part) {
			t.Errorf("query %q missing %q", command[1:], part)
			return false
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{byte(len(types))}) {
		return false
	}
	for _, typ := range types {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("v", typ, 63, 0)) {
			return false
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) {
		return false
	}
	for _, values := range rows {
		var row []byte
		for _, value := range values {
			if value == nil {
				row = append(row, 251)
				continue
			}
			var b []byte
			switch v := value.(type) {
			case string:
				b = []byte(v)
			case []byte:
				b = v
			default:
				t.Fatalf("bad table fixture %T", value)
			}
			row = append(row, LobLengthPrefix(len(b))...)
			row = append(row, b...)
		}
		if !testpeer.SendPackets(ctx, t, p, row) {
			return false
		}
	}
	return testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0})
}

// ServeTxSteps is a shared public API protocol fixture.
func ServeTxSteps(ctx context.Context, t *testing.T, p *wire.PacketIO, steps []TxStep) bool {
	for _, step := range steps {
		if !testpeer.ServeOK(ctx, t, p, append([]byte{3}, step.Query...), step.Status) {
			return false
		}
	}
	return true
}

// CommandPacket is a shared public API protocol fixture.
func CommandPacket(affected, id uint64, status byte) []byte {
	data := binary.LittleEndian.AppendUint64([]byte{254}, affected)
	data = binary.LittleEndian.AppendUint64(append(data, 254), id)
	return append(append([]byte{0}, data...), status, 0, 0, 0)
}

// ServeLOBQuery is a shared public API protocol fixture.
func ServeLOBQuery(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, typ byte, values ...[]byte) bool {
	return ServeLOBQueryStatus(ctx, t, p, query, typ, 6, values...)
}

// ServeLOBQueryStatus is a shared public API protocol fixture.
func ServeLOBQueryStatus(ctx context.Context, t testing.TB, p *wire.PacketIO, query string, typ, status byte, values ...[]byte) bool {
	t.Helper()
	if !testpeer.ReadQuery(ctx, t, p, query) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("lob", typ, 45, 0), []byte{254, 0, 0, status, 0}) {
		return false
	}
	for _, value := range values {
		row := []byte{251}
		if value != nil {
			row = append(LobLengthPrefix(len(value)), value...)
		}
		if !testpeer.SendPackets(ctx, t, p, row) {
			return false
		}
	}
	return testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0})
}

// TestCursorEpochPlain selects uncompressed fixture framing.
const TestCursorEpochPlain = "plain"

// TxDB is a shared public API protocol fixture.
func TxDB(t *testing.T, mode config.Mode, serve func(context.Context, *wire.PacketIO), configure ...func(*config.Config)) *sql.DB {
	t.Helper()
	status := byte(2)
	if mode == config.ModeOracle {
		status = 6
	}
	cfg := config.Config{User: TestUser, Mode: mode, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, status) || !testpeer.ServeBaseline(ctx, t, p, status) {
			return
		}
		serve(context.Background(), p)
	})}
	for _, apply := range configure {
		apply(&cfg)
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(connector)
}

// BaseTable is the dictionary label for a persistent base table.
const BaseTable = "BASE TABLE"
