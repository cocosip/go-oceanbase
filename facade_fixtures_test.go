package oceanbase

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	testing "testing"

	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testUser       = "app"
	testPassword   = "secret"
	testServerName = "db.example"
	readySQL       = "SET @ready=1"

	testProxyCapability = "ob_capability_flag"
	testOB20            = "ob20"
	testCompression     = "compression"
	testStandard        = "standard"

	transactionReadOnlyWrite  = "UPDATE readonly_target SET value=1"
	transactionPendingWrite   = "UPDATE pending SET value=1"
	transactionMySQLReadOnly  = "START TRANSACTION READ ONLY"
	transactionOracleReadOnly = "SET TRANSACTION READ ONLY"
	transactionUTF8Names      = "SET NAMES utf8mb4"
	transactionUTCZone        = "SET SESSION time_zone = '+00:00'"
	transactionMySQLLabel     = "mysql"
	transactionOracleLabel    = "oracle"
)

var (
	testTLSConfig           = testpeer.TLSConfig
	authGreeting            = testpeer.AuthGreeting
	readAuthResponse        = testpeer.ReadAuthResponse
	peerDial                = testpeer.PeerDial
	serveConnect            = testpeer.ServeConnect
	serveOK                 = testpeer.ServeOK
	serveBaseline           = testpeer.ServeBaseline
	serveQuit               = testpeer.ServeQuit
	serveCreateDatabaseAuth = testpeer.ServeCreateDatabaseAuth
	sendPackets             = testpeer.SendPackets
	protocolOK              = testpeer.ProtocolOK
	readCommand             = testpeer.ReadCommand
)

// The peer keeps the test-wide deadline; a client close must not fail deadline cleanup after a complete write.
type authPeerConn = testpeer.AuthPeerConn

func queryDB(t *testing.T, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	c, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		serve(context.Background(), p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

var _ driver.Connector = (*connector)(nil)
