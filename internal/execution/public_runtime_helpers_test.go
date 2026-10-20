package execution_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"io"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testProxyCapability = "ob_capability_flag"
	testOB20            = "ob20"
	testCompression     = "compression"
	testStandard        = "standard"
)

const localFilenameCharset = "gbk"

const (
	lobOpenEpochQuery   = "SELECT lob FROM lob_open_epoch WHERE id=?"
	lobOpenEpochLiteral = "SELECT lob FROM lob_open_epoch WHERE id= 1 "
	lobOpenText         = "text"
	lobOpenClient       = "client"
	lobOpenServer       = "server"
	lobOpenCombined     = "combined"
	lobOpenPrepare      = "prepare"
	lobOpenMetadata     = "metadata"
	lobOpenFinal        = "final"
)

const selectBindSQL = "SELECT ?"

const (
	identityPrivate      = "private-identity"
	identityUser         = "测试@tenant"
	identityGBKDatabase  = "\xd3\xa6\xd3\xc3"
	identityCharsetLatin = "latin1"
	identityCharsetASCII = "ascii"
)

const testAttributeGB18030 = "gb18030"

const (
	parameterBudgetText      = "测测"
	parameterBudgetGBK       = "\xb2\xe2\xb2\xe2"
	parameterBudgetExpansion = "\x81\x30\x81\x30\x81\x30\x81\x30"
)

const sqlBudgetSelect = "SELECT '测测'"

type commandReadDeadlineConn struct {
	net.Conn
	timeout *atomic.Int64
}

func (c commandReadDeadlineConn) Read(data []byte) (int, error) {
	if timeout := time.Duration(c.timeout.Load()); timeout > 0 {
		if err := c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Read(data)
}

const testUser = "app"

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

const testBatchSQL = "INSERT INTO t VALUES (?)"

const optimizedSelect = "SELECT ? FROM dual"

const testLOBABC = "abc"

func lobInlineFixture(value string) []byte {
	return append([]byte{'!', 'B', 'O', 'L', 2, 0xa0, 0, 0}, value...)
}

type longChanReader chan byte

func (longChanReader) Read([]byte) (int, error) { return 0, io.EOF }

func longDataPacket(mode oceanbase.Mode, id uint32, index uint16, piece byte, data []byte) []byte {
	b := binary.LittleEndian.AppendUint16(publictest.StmtPacket(24, id), index)
	if mode == oceanbase.ModeOracle {
		b[0] = 162
		b = append(b, piece, 0)
		b = binary.LittleEndian.AppendUint64(b, uint64(len(data)))
	}
	return append(b, data...)
}

type longReaderFunc func([]byte) (int, error)

func (f longReaderFunc) Read(p []byte) (int, error) { return f(p) }

type longMapReader map[string]byte

func (longMapReader) Read([]byte) (int, error) { return 0, io.EOF }

type longSliceReader []byte

func (longSliceReader) Read([]byte) (int, error) { return 0, io.EOF }

const localImportSQL = "LOAD DATA LOCAL INFILE 'input' INTO TABLE t"

func localInfileDB(t *testing.T, mode oceanbase.Mode, serve func(context.Context, *wire.PacketIO), configure ...func(*oceanbase.Config)) *sql.DB {
	t.Helper()
	cfg := oceanbase.Config{User: testUser, Mode: mode}
	cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		greeting := testpeer.AuthGreeting(authentication.Native, false)
		compressed := cfg.Protocol.Compression == oceanbase.FeatureRequired
		ob20 := cfg.Protocol.OB20 == oceanbase.FeatureRequired
		if compressed || ob20 {
			greeting = testpeer.ProtocolGreeting(compressed)
		}
		offset := bytes.IndexByte(greeting[1:], 0) + 1 + 1 + 4 + 8 + 1
		greeting[offset] |= 128
		if !testpeer.SendPackets(ctx, t, p, greeting) {
			return
		}
		response, err := p.ReadMessage(ctx)
		if err != nil || len(response) < 4 || binary.LittleEndian.Uint32(response)&128 == 0 {
			t.Error("local infile capability mismatch", err)
			return
		}
		status := byte(2)
		if mode == oceanbase.ModeOracle {
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

func localRequest(ctx context.Context, t *testing.T, p *wire.PacketIO, name string) bool {
	t.Helper()
	return testpeer.ReadQuery(ctx, t, p, localImportSQL) && testpeer.SendPackets(ctx, t, p, append([]byte{251}, name...))
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

const localInputName = "input"

const (
	testXAServerPath = "xa-server"
	testXAStartSQL   = "XA START X'67',X'',0x1"
	testXAEndSQL     = "XA END X'67',X'',0x1"
	testXAIdentity   = "xa_identity"
)

const testManualTxStart = "START TRANSACTION"

const (
	testAutocommitOn    = "SET autocommit=1"
	testMySQLReadWrite  = "START TRANSACTION READ WRITE"
	testSerializableSQL = "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
	testAutocommitOff   = "SET autocommit=0"
	testCommitSQL       = "COMMIT"
	testRollbackSQL     = "ROLLBACK"
	testOracleReadWrite = "SET TRANSACTION READ WRITE"
)

const localMalformedNameStage = "malformed-infile-name"

const localErrorStage = "server-error"

const localReaderStage = "source-reader"

const localEmptyStage = "empty-source"

const localNilStage = "nil-source"

type localOwnedReader struct {
	io.Reader
	closed   int
	closeErr error
}

func (r *localOwnedReader) Close() error { r.closed++; return r.closeErr }

const localSuccessStage = "source-success"

const localTypedNilStage = "typed-nil-source"

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

const testClientStrategy = "client"

const testServerStrategy = "server"

const testMixedSQL = "UPDATE t SET v=1; SELECT v; UPDATE t SET v=2"

const testMaxUint64Text = "18446744073709551615"

func assertInitialExecution(t *testing.T, s oceanbase.ExecutionSnapshot) {
	t.Helper()
	if s.Done || len(s.Results) != 2 || s.Results[0].Kind != oceanbase.ResultCommand || !s.Results[0].RowsAffectedKnown || s.Results[0].RowsAffected != 0 || s.Results[0].InsertIDKnown || s.Results[0].RowSetIndex != -1 || s.Results[1].RowSetIndex != 0 || s.Results[1].Complete {
		t.Fatal(s)
	}
}

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

const (
	executionColumnQuery   = "SELECT t.base AS visible FROM source_table t"
	executionColumnCatalog = "def"
	executionColumnOrigin  = "base"
)

func activeRowsConn(t *testing.T) (publictest.RawConn, driver.Rows, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	peerClosed := make(chan struct{})
	cfg := oceanbase.Config{User: testUser, Limits: oceanbase.Limits{CleanupTimeout: 20 * time.Millisecond}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		defer close(peerClosed)
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ReadQuery(context.Background(), t, p, "SELECT waiting") {
			return
		}
		if !testpeer.SendPackets(context.Background(), t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		if _, err := p.ReadMessage(context.Background()); err == nil {
			t.Error("active response was interleaved")
		}
	})}
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r, err := dc.(publictest.RawConn).QueryContext(ctx, "SELECT waiting", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = r.Close(); _ = dc.Close() })
	return dc.(publictest.RawConn), r, cancel, peerClosed
}
