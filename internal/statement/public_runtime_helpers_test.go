package statement_test

import (
	bytes "bytes"
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	hex "encoding/hex"
	io "io"
	net "net"
	reflect "reflect"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	charset "github.com/cocosip/go-oceanbase/internal/charset"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	testutil "github.com/cocosip/go-oceanbase/internal/testutil"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func optimizationDB(t *testing.T, mode oceanbase.Mode, options oceanbase.ExecutionOptions, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	status := byte(2)
	if mode == oceanbase.ModeOracle {
		status = 6
	}
	connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Mode: mode, Execution: options, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
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

const testUser = "app"

func optimizationExecute(id uint32, value int64, reuse bool) []byte {
	b := append(publictest.ExecuteHeader(id), 0, 1)
	if reuse {
		b[len(b)-1] = 0
	} else {
		b = append(b, 8, 0)
	}
	return binary.LittleEndian.AppendUint64(b, uint64(value))
}

const testBatchSQL = "INSERT INTO t VALUES (?)"

const selectBindSQL = "SELECT ?"

type closeFailureConn struct{ net.Conn }

func (c closeFailureConn) Write(data []byte) (int, error) {
	if len(data) == 5 && data[0] == 25 {
		return 0, testutil.ErrInjected
	}
	return c.Conn.Write(data)
}

const testCacheSQL = "INSERT INTO u VALUES (?)"

const (
	testAutocommitOn    = "SET autocommit=1"
	testMySQLReadWrite  = "START TRANSACTION READ WRITE"
	testSerializableSQL = "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
	testAutocommitOff   = "SET autocommit=0"
	testCommitSQL       = "COMMIT"
	testRollbackSQL     = "ROLLBACK"
	testOracleReadWrite = "SET TRANSACTION READ WRITE"
)

const (
	testProxyCapability = "ob_capability_flag"
	testOB20            = "ob20"
	testCompression     = "compression"
	testStandard        = "standard"
)

func batchExecutePacket(t *testing.T, value int64) []byte {
	t.Helper()
	p, err := valuecodec.EncodeParameter(value, oceanbase.ModeMySQL, oceanbase.SessionOptions{}, 64)
	if err != nil {
		t.Fatal(err)
	}
	b, err := wire.EncodeStmtExecute(7, []wire.Parameter{p}, 128)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func longDataPacket(mode oceanbase.Mode, id uint32, index uint16, piece byte, data []byte) []byte {
	b := binary.LittleEndian.AppendUint16(publictest.StmtPacket(24, id), index)
	if mode == oceanbase.ModeOracle {
		b[0] = 162
		b = append(b, piece, 0)
		b = binary.LittleEndian.AppendUint64(b, uint64(len(data)))
	}
	return append(b, data...)
}

// Model cancellation whose queued callbacks are stopped before the scheduler runs them.
type longQueuedContext struct {
	context.Context
	done          chan struct{}
	canceled      atomic.Bool
	registrations atomic.Int32
	cancelAt      int32
}

func (c *longQueuedContext) cancel() {
	if c.canceled.CompareAndSwap(false, true) {
		close(c.done)
	}
}

func (c *longQueuedContext) AfterFunc(func()) func() bool {
	if c.registrations.Add(1) == c.cancelAt {
		c.cancel()
	}
	return func() bool { return true }
}

func (c *longQueuedContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func (c *longQueuedContext) Done() <-chan struct{} { return c.done }

type longCancelConn struct {
	net.Conn
	written int
	cancel  func()
}

func (c *longCancelConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written += n
	if c.written >= 13 {
		c.cancel()
	}
	return n, err
}

func outputTestConfig() oceanbase.Config {
	return oceanbase.Config{Session: oceanbase.SessionOptions{Charset: "utf8mb4", Location: time.UTC}, Limits: oceanbase.Limits{MaxMessageBytes: 4096, MaxLOBBytes: 4096, MaxResultItems: 16, CleanupTimeout: time.Second}}
}

type longReaderFunc func([]byte) (int, error)

func (f longReaderFunc) Read(p []byte) (int, error) { return f(p) }

func serveLongContent(ctx context.Context, t *testing.T, p *wire.PacketIO, mode oceanbase.Mode, id uint32, index uint16, text bool, want []byte) bool {
	t.Helper()
	var got []byte
	for {
		p.ResetSequence()
		b, err := p.ReadMessage(ctx)
		header := 7
		if mode == oceanbase.ModeOracle {
			header = 17
		}
		if err != nil || len(b) < header || binary.LittleEndian.Uint32(b[1:]) != id || binary.LittleEndian.Uint16(b[5:]) != index {
			t.Error("invalid long data packet", err)
			return false
		}
		data := b[header:]
		if mode == oceanbase.ModeOracle {
			if b[0] != 162 || b[8] != 0 || binary.LittleEndian.Uint64(b[9:]) != uint64(len(data)) {
				t.Error("invalid piece header")
				return false
			}
			if !text {
				data, err = hex.DecodeString(string(data))
				if err != nil {
					t.Error(err)
					return false
				}
			}
			if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
				return false
			}
		} else if b[0] != 24 {
			t.Error("not LONG_DATA")
			return false
		}
		got = append(got, data...)
		if len(got) > len(want) {
			t.Error("too much data")
			return false
		}
		if mode == oceanbase.ModeOracle && b[7] == 3 || mode != oceanbase.ModeOracle && len(got) == len(want) {
			break
		}
	}
	if !bytes.Equal(got, want) {
		t.Error("stream content mismatch")
		return false
	}
	return true
}

type longErrorReader struct {
	data []byte
	err  error
}

func (r *longErrorReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

type longTestReader struct {
	io.Reader
	closed int
}

func (r *longTestReader) Close() error { r.closed++; return nil }

const (
	executionColumnQuery   = "SELECT t.base AS visible FROM source_table t"
	executionColumnCatalog = "def"
	executionColumnOrigin  = "base"
)

func assertExecutionColumnRow(t *testing.T, e *oceanbase.Execution, want oceanbase.ResultColumn) {
	t.Helper()
	types, err := e.Rows().ColumnTypes()
	if err != nil || len(types) != 1 || types[0].Name() != want.Name {
		t.Fatal("standard column label changed", types, err)
	}
	if !e.Rows().Next() {
		t.Fatal(e.Rows().Err())
	}
	var value int
	if err = e.Rows().Scan(&value); err != nil || value != 7 {
		t.Fatal(value, err)
	}
	if err = e.Close(); err != nil || !e.Snapshot().Results[0].Complete || !reflect.DeepEqual(e.Snapshot().Results[0].Columns, []oceanbase.ResultColumn{want}) {
		t.Fatal("columns lost after close", err)
	}
}

const optimizedSelect = "SELECT ? FROM dual"

func executionColumnPacket(t *testing.T, column oceanbase.ResultColumn, encoding string) []byte {
	t.Helper()
	var data []byte
	for _, value := range []string{column.Catalog, column.Schema, column.Table, column.OriginalTable, column.Name, column.OriginalName} {
		encoded, err := charset.Encode(encoding, value)
		if err != nil || len(encoded) > 250 {
			t.Fatal("invalid test column", err)
		}
		data = append(data, byte(len(encoded)))
		data = append(data, encoded...)
	}
	return append(data, 12, 28, 0, 11, 0, 0, 0, 3, 0, 0, 0, 0, 0)
}

const testUTF8MB4 = "utf8mb4"
