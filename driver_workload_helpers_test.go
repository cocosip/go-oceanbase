package oceanbase_test

import (
	context "context"
	driver "database/sql/driver"
	binary "encoding/binary"
	net "net"
	atomic "sync/atomic"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valuecodec "github.com/cocosip/go-oceanbase/internal/valuecodec"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

type driverPhysical interface {
	driver.Conn
	driver.Validator
}

type driverTrackedConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *driverTrackedConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func driverFixtureBatchArrayPacket(t testing.TB, values []any, options wire.ExecuteOptions) []byte {
	t.Helper()
	rows := make([][]wire.Parameter, len(values))
	for i, value := range values {
		parameter, err := valuecodec.EncodeParameter(value, oceanbase.ModeOracle, oceanbase.SessionOptions{}, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		rows[i] = []wire.Parameter{parameter}
	}
	packet, err := wire.EncodeStmtExecuteArray(7, rows, 1<<20, options)
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

const driverFixtureBatchRewriteGuard = "SELECT MAX(COALESCE(LOWER(VALUE),'')!='false') FROM oceanbase.GV$OB_PARAMETERS WHERE NAME='ob_enable_batched_multi_statement'"

func driverFixtureCursorExecuteFixture(id uint32) []byte {
	b := publictest.ExecuteHeader(id)
	b[5] = 1
	return b
}

func driverFixtureOptimizationExecute(id uint32, value int64, reuse bool) []byte {
	b := append(publictest.ExecuteHeader(id), 0, 1)
	if reuse {
		b[len(b)-1] = 0
	} else {
		b = append(b, 8, 0)
	}
	return binary.LittleEndian.AppendUint64(b, uint64(value))
}

func driverFixtureServeBatchRewriteGuard(ctx context.Context, t testing.TB, p *wire.PacketIO, value string, status byte) bool {
	t.Helper()
	return testpeer.ReadQuery(ctx, t, p, driverFixtureBatchRewriteGuard) && testpeer.SendPackets(ctx, t, p,
		[]byte{1}, testpeer.TextColumn("server-merge-on", 253, 45, 0), []byte{254, 0, 0, status, 0},
		append([]byte{byte(len(value))}, value...), []byte{254, 0, 0, status, 0})
}

const (
	driverFixtureAutocommitOn    = "SET autocommit=1"
	driverFixtureMySQLReadWrite  = "START TRANSACTION READ WRITE"
	driverFixtureAutocommitOff   = "SET autocommit=0"
	driverFixtureCommitSQL       = "COMMIT"
	driverFixtureRollbackSQL     = "ROLLBACK"
	driverFixtureOracleReadWrite = "SET TRANSACTION READ WRITE"
)

const (
	driverFixtureOB20        = "ob20"
	driverFixtureCompression = "compression"
	driverFixtureStandard    = "standard"
)

const driverFixtureOctetLength = "OCTET_LENGTH"

const driverFixtureUser = "app"
