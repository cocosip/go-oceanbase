package connect_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/connect"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

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

const testSessionVariable = "wait_timeout"

const testReadSessionVariable = "SELECT @@SESSION.wait_timeout"

const testUser = "app"

const readySQL = "SET @ready=1"

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

func serveCancelConnect(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte, id uint32) bool {
	t.Helper()
	greeting := testpeer.AuthGreeting(authentication.Native, false)
	binary.LittleEndian.PutUint32(greeting[bytes.IndexByte(greeting, 0)+1:], id)
	return testpeer.SendPackets(ctx, t, p, greeting) && testpeer.ReadAuthResponse(ctx, t, p) && testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, status, 0, 0, 0})
}

const (
	testCreateDatabaseMode    = "oracle-mode"
	testCreateDatabaseTimeout = "timeout"
	testCreateDatabaseName    = "example"
	testCreateDatabaseQuery   = "\x03CREATE DATABASE IF NOT EXISTS `example`"
	testCreateDatabaseSelect  = "\x02example"
)

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

//nolint:unparam // Preserve explicit wire fields in the shared protocol fixture.
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

func executeHeader(id uint32) []byte { return append(stmtPacket(23, id), 0, 1, 0, 0, 0) }

func stmtPacket(cmd byte, id uint32) []byte { return binary.LittleEndian.AppendUint32([]byte{cmd}, id) }

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

const changeUserPassword = "next-secret"

const changeUserName = "next@tenant"

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

//nolint:unparam // Preserve explicit wire fields in the shared protocol fixture.
func commandPacket(affected, id uint64, status byte) []byte {
	data := binary.LittleEndian.AppendUint64([]byte{254}, affected)
	data = binary.LittleEndian.AppendUint64(append(data, 254), id)
	return append(append([]byte{0}, data...), status, 0, 0, 0)
}

const testCharsetUTF16 = "utf16"

func runPlatformTLSGate(t *testing.T, listener net.Listener, cfg config.Config) {
	t.Helper()
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := listener.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = raw.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		p := wire.NewPacketIO(raw, 64<<20)
		if err = p.WriteMessage(ctx, testpeer.AuthGreeting(authentication.Clear, false)); err != nil {
			t.Error(err)
			return
		}
		if response, e := p.ReadMessage(ctx); e == nil || len(response) != 0 {
			t.Error("IPC sent plaintext credentials")
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	created, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = created.Connect(context.Background()); !errors.Is(err, dberrors.ErrTLSRequired) {
		t.Fatal("IPC got plaintext exemption", err)
	}
}

const testPassword = "secret"

func runAuthConnTest(t *testing.T, cfg config.Config, wrap func(net.Conn) net.Conn, serve func(context.Context, net.Conn, *wire.PacketIO)) (*session.Negotiated, error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	if err := server.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(ctx, server, wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20))
	}()
	created, err := config.Normalize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	transport := client
	if wrap != nil {
		transport = wrap(client)
	}
	result, err := connect.Authenticate(ctx, created, transport)
	select {
	case <-done:
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		<-done
		t.Fatal("protocol peer did not finish")
	}
	return result, err
}

// tlsExampleConfig is application example code, not a driver certificate-store API.
//
//nolint:unparam // Preserve explicit wire fields in the shared protocol fixture.
func tlsExampleConfig(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no valid CA certificate")
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12, ServerName: serverName,
		RootCAs: roots, Certificates: []tls.Certificate{certificate},
	}, nil
}

func tlsClientFiles(t *testing.T, server *tls.Config) (string, string, string) {
	t.Helper()
	rootDER := server.Certificates[0].Certificate[0]
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture's root must authorize both uses; the shared server-only fixture stays unchanged.
	root.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	rootDER, err = x509.CreateCertificate(rand.Reader, root, root, root.PublicKey, server.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	server.Certificates[0].Certificate[0] = rootDER
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: tlsClientName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, server.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caFile, certFile, keyFile := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "client.pem"), filepath.Join(dir, "client-key.pem")
	for name, data := range map[string][]byte{
		caFile: pem.EncodeToMemory(&pem.Block{Type: tlsCertificatePEM, Bytes: rootDER}),
		certFile: append(pem.EncodeToMemory(&pem.Block{Type: tlsCertificatePEM, Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: tlsCertificatePEM, Bytes: rootDER})...),
		keyFile: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	} {
		if err = os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return caFile, certFile, keyFile
}

const testServerName = "db.example"

const tlsCertificatePEM = "CERTIFICATE"

const tlsClientName = "oceanbase-app"

const tlsCertificateCallbackPath = "certificate-callback"

const tlsVerifyCallbackPath = "verify-callback"

const routeFirst = "first.example:2881"

const routeSecond = "second.example:2881"

func serveProxyIdentity(ctx context.Context, t *testing.T, p *wire.PacketIO, id uint32) bool {
	t.Helper()
	greeting := testpeer.ProtocolGreeting(false)
	binary.LittleEndian.PutUint32(greeting[bytes.IndexByte(greeting, 0)+1:], id)
	return testpeer.SendPackets(ctx, t, p, greeting) && testpeer.ReadAuthResponse(ctx, t, p) && testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 6))
}

const testProxyUser = "client_user"

const (
	testProxyCapability = "ob_capability_flag"
	testOB20            = "ob20"
	testCompression     = "compression"
	testStandard        = "standard"
)

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

const testBatchSQL = "INSERT INTO t VALUES (?)"

const (
	testAutocommitOn    = "SET autocommit=1"
	testMySQLReadWrite  = "START TRANSACTION READ WRITE"
	testSerializableSQL = "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
	testAutocommitOff   = "SET autocommit=0"
	testCommitSQL       = "COMMIT"
	testRollbackSQL     = "ROLLBACK"
	testOracleReadWrite = "SET TRANSACTION READ WRITE"
)

const discoveryTestService = "orders"

const discoveryTestHost = "first"

const discoveryTestAddr = "first:2881"

type dialResultConn struct {
	net.Conn
	closes int
}

const routeDialOperation = "connect"

const localFilenameCharset = "gbk"

func runAuthTest(t *testing.T, cfg config.Config, serve func(context.Context, *wire.PacketIO)) (*session.Negotiated, error) {
	t.Helper()
	return runAuthConnTest(t, cfg, nil, func(ctx context.Context, _ net.Conn, p *wire.PacketIO) { serve(ctx, p) })
}

const testUTCOffset = "+00:00"

const testAttributeValue = "billing,worker:1"

func serveAttributeBaseline(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte, charset string) bool {
	t.Helper()
	return testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES "+charset), status) && testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=1"), status) && testpeer.ServeVersion(ctx, t, p, status, false) && testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status)
}

const testDriverDialectAttribute = "__mysql_client_type"

const testAttributeGBKValue = "\xb2\xe2\xca\xd4"

const testAttributeKey = "application"

const testClientAttributeCapability = "__ob_client_attribute_capability_flag"

const testProxyRequestCapability = "__proxy_capability_flag"

const testDriverDialectValue = "__ob_jdbc_client"

// authExampleCredentials is application code; the driver never reads environment credentials.
func authExampleCredentials(ctx context.Context) (config.Credentials, error) {
	if err := ctx.Err(); err != nil {
		return config.Credentials{}, err
	}
	user, userSet := os.LookupEnv("OB_USER")
	password, passwordSet := os.LookupEnv("OB_PASSWORD")
	if !userSet || user == "" || !passwordSet {
		return config.Credentials{}, errors.New("OB_USER and OB_PASSWORD must be set")
	}
	return config.Credentials{User: user, Password: password, ProxyUser: os.Getenv("OB_PROXY_USER")}, nil
}

func checkPinnedCipher(t *testing.T, key *rsa.PrivateKey, ciphertext []byte, nonce, password string) bool {
	t.Helper()
	plain, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, key, ciphertext, nil)
	if err != nil {
		t.Error("ciphertext cannot be decrypted by provisioned key", err)
		return false
	}
	for i := range plain {
		plain[i] ^= nonce[i%len(nonce)]
	}
	if !bytes.Equal(plain, append([]byte(password), 0)) {
		t.Error("RSA response used wrong nonce or credentials")
		return false
	}
	return true
}

func testAuthNativeRSAPath(t *testing.T, key *rsa.PrivateKey, mode config.Mode, plugin, path string) {
	t.Helper()
	status := byte(2)
	if mode == config.ModeOracle {
		status = 6
	}
	var calls atomic.Int32
	public := &rsa.PublicKey{N: new(big.Int).Set(key.N), E: key.E}
	configured, err := oceanbase.NewConnector(config.Config{Mode: mode, Auth: config.AuthOptions{RSAKey: public}, Credentials: func(context.Context) (config.Credentials, error) {
		calls.Add(1)
		return config.Credentials{User: testUser, Password: testPassword}, nil
	}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		serveNativeRSAPeer(ctx, t, p, key, mode, plugin, path, status)
	})})
	if err != nil {
		t.Fatal(err)
	}
	// Mutation after construction must not change the key used on the wire.
	public.N.SetInt64(1)
	public.E = 2
	if calls.Load() != 0 {
		t.Fatal("constructor obtained credentials")
	}
	db := sql.OpenDB(configured)
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if path == authRSAChangePath {
		if err = oceanbase.ChangeUser(context.Background(), pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err = pinned.PingContext(context.Background()); err != nil || calls.Load() != 1 {
		t.Fatal("RSA session or credential lifecycle", err, calls.Load())
	}
}

const authRSAChangePath = "change"

func serveNativeRSAPeer(ctx context.Context, t *testing.T, p *wire.PacketIO, key *rsa.PrivateKey, mode config.Mode, plugin, path string, status byte) {
	t.Helper()
	password, nonce := testPassword, "12345678901234567890"
	if path == "initial" {
		if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(plugin, false)) {
			return
		}
		response, e := p.ReadMessage(ctx)
		if e != nil || len(response) < 36 || string(response[32:36]) != "app\x00" {
			t.Error("missing RSA handshake", e)
			return
		}
		if plugin == authentication.SHA256 {
			if len(response) < 295 || !bytes.Equal(response[36:39], []byte{252, 0, 1}) || !checkPinnedCipher(t, key, response[39:295], nonce, password) {
				t.Error("missing length-encoded RSA ciphertext")
				return
			}
		}
	} else {
		if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Native, false)) || !testpeer.ReadAuthResponse(ctx, t, p) {
			return
		}
		if path == authRSAChangePath {
			if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, status)) || !testpeer.ServeBaseline(ctx, t, p, status) {
				return
			}
			ctx = context.Background()
			if !testpeer.ReadCommand(ctx, t, p, changeUserPacket(t, mode, "")) {
				return
			}
			password = changeUserPassword
		}
		nonce = "abcdefghijklmnopqrst"
		switchPacket := append(append(append([]byte{254}, plugin...), 0), nonce...)
		if !testpeer.SendPackets(ctx, t, p, append(switchPacket, 0)) {
			return
		}
		response, e := p.ReadMessage(ctx)
		if e != nil {
			t.Error(e)
			return
		}
		if plugin == authentication.SHA256 && !checkPinnedCipher(t, key, response, nonce, password) {
			return
		}
	}
	if plugin == authentication.CachingSHA2 {
		if !testpeer.SendPackets(ctx, t, p, []byte{1, 4}) {
			return
		}
		ciphertext, e := p.ReadMessage(ctx)
		if e != nil || !checkPinnedCipher(t, key, ciphertext, nonce, password) {
			t.Error("missing full-auth RSA response", e)
			return
		}
	}
	if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, status)) || !testpeer.ServeBaseline(ctx, t, p, status) {
		return
	}
	ctx = context.Background()
	if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func driverInstance() driver.Driver {
	c, err := oceanbase.NewConnector(config.Config{User: testUser})
	if err != nil {
		panic(err)
	}
	return c.Driver()
}

const networkTCP = "tcp"
const networkUnix = "unix"
const sessionReadDualSuffix = " FROM dual"

func (c *dialResultConn) Close() error { c.closes++; return c.Conn.Close() }

const testUTF8MB4 = "utf8mb4"
