package connect_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	binary "encoding/binary"
	errors "errors"
	fmt "fmt"
	io "io"
	net "net"
	filepath "path/filepath"
	runtime "runtime"
	strconv "strconv"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	transport "github.com/cocosip/go-oceanbase/internal/transport"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func runMixedSessionCycle(ctx context.Context, db stabilitySession, i int) error {
	for _, value := range []int64{int64(i + 1), -int64(i + 1)} {
		result, err := db.ExecContext(ctx, sessionStabilityUpdate, value)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("cached update count %d: %v", n, err)
		}
	}
	rows, err := db.QueryContext(ctx, sessionStabilitySelect)
	if err != nil {
		return err
	}
	if i%2 != 0 {
		var value int64
		if !rows.Next() || rows.Scan(&value) != nil || value != int64(i+1) {
			_ = rows.Close()
			return fmt.Errorf("mixed rows value %d: %v", value, rows.Err())
		}
	}
	if err = rows.Close(); err != nil || rows.Err() != nil {
		return fmt.Errorf("early Rows.Close: %v, rows: %v", err, rows.Err())
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, sessionStabilityMutation)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if n, countErr := result.RowsAffected(); countErr != nil || n != 1 {
		_ = tx.Rollback()
		return fmt.Errorf("transaction update count %d: %v", n, countErr)
	}
	if i%2 == 0 {
		err = tx.Rollback()
	} else {
		err = tx.Commit()
	}
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, sessionStabilityUpdate, i+1)
	var server *oceanbase.Error
	if !errors.As(err, &server) || server.Code != 1062 || server.SQLState != "23000" {
		return fmt.Errorf("recoverable prepared error: %v", err)
	}
	if err = db.PingContext(ctx); err != nil {
		return fmt.Errorf("session not reusable after mixed cycle: %w", err)
	}
	return nil
}

type stabilitySession interface {
	oceanbase.Session
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	PingContext(context.Context) error
}

const (
	sessionStabilityUpdate   = "UPDATE stability_fixture SET v=?"
	sessionStabilitySelect   = "SELECT v FROM stability_fixture"
	sessionStabilityMutation = "UPDATE stability_fixture SET v=1"
)

func runPlatformMixedSession(t *testing.T, listener net.Listener, cfg config.Config, framing string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	status := byte(2)
	if cfg.Mode == config.ModeOracle {
		status = 6
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		server, err := listener.Accept()
		if err != nil {
			if !t.Failed() {
				t.Error(err)
			}
			return
		}
		defer func() { _ = server.Close() }()
		if err = server.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20)
		if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, framing, status) || !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
			return
		}
		for i := range 2 {
			if !serveDriverSessionCycle(ctx, t, p, cfg.Mode, i, nil) {
				return
			}
		}
		if !testpeer.ReadCommand(ctx, t, p, []byte{1}) {
			return
		}
		// Windows AF_UNIX can omit peer EOF even after a successful net.UnixConn.Close.
		if runtime.GOOS == "windows" && cfg.Network == networkUnix {
			return
		}
		if _, err = p.ReadMessage(ctx); !errors.Is(err, io.EOF) {
			t.Error("native IPC peer did not observe EOF after QUIT", err)
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	var rawTransport net.Conn
	cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var err error
		rawTransport, err = transport.Dial(ctx, network, addr)
		return rawTransport, err
	}
	cfg.User = testUser
	cfg.Execution = config.ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}
	cfg.Protocol = sessionStabilityProtocol(framing)
	created, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(created)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err = db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var physical driver.Validator
	err = pinned.Raw(func(raw any) error { physical = raw.(driver.Validator); return nil })
	closeErr := pinned.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	for i := range 2 {
		if err = runMixedSessionCycle(ctx, db, i); err != nil {
			t.Fatal(err)
		}
		checkMixedPoolIdle(t, db.Stats())
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := db.Stats(); stats.OpenConnections != 0 || stats.InUse != 0 || physical.IsValid() {
		t.Fatal("native IPC did not release its pool and original physical session", stats)
	}
	if cfg.Network == networkUnix {
		if err = rawTransport.SetDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
			t.Fatal("native IPC underlying transport is still open after DB.Close", err)
		}
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("native IPC peer did not finish its QUIT handling")
	}
}

func sessionStabilityProtocol(framing string) config.ProtocolOptions {
	protocol := config.ProtocolOptions{Compression: config.FeatureOff, OB20: config.FeatureOff}
	if framing == testCompression {
		protocol.Compression = config.FeatureRequired
	}
	if framing == testOB20 {
		protocol.OB20 = config.FeatureRequired
	}
	return protocol
}

func serveDriverSessionCycle(ctx context.Context, t *testing.T, p *wire.PacketIO, mode config.Mode, i int, barrier func() bool) bool {
	t.Helper()
	status, active := byte(2), byte(3)
	begin := []txStep{{testMySQLReadWrite, active}}
	if mode == config.ModeOracle {
		status, active = 6, 5
		begin = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, active}}
	}
	id, value := uint32(2*i+1), int64(i+1)
	if !servePrepareStatus(ctx, t, p, sessionStabilityUpdate, id, 1, nil, status) ||
		!testpeer.ReadCommand(ctx, t, p, optimizationExecute(id, value, false)) {
		return false
	}
	if barrier != nil && !barrier() {
		return false
	}
	if !testpeer.SendPackets(ctx, t, p, commandPacket(1, 0, status)) ||
		!testpeer.ReadCommand(ctx, t, p, optimizationExecute(id, -value, true)) || !testpeer.SendPackets(ctx, t, p, commandPacket(1, 0, status)) ||
		!testpeer.ReadCommand(ctx, t, p, stmtPacket(25, id)) || !testpeer.ReadQuery(ctx, t, p, sessionStabilitySelect) ||
		!testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, status, 0}) {
		return false
	}
	for row := 0; row < 3; row++ {
		text := strconv.Itoa(i + row + 1)
		if !testpeer.SendPackets(ctx, t, p, append([]byte{byte(len(text))}, text...)) {
			return false
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) || !serveTxSteps(ctx, t, p, begin) ||
		!testpeer.ReadQuery(ctx, t, p, sessionStabilityMutation) || !testpeer.SendPackets(ctx, t, p, commandPacket(1, 0, active)) {
		return false
	}
	finish := testCommitSQL
	if i%2 == 0 {
		finish = testRollbackSQL
	}
	end := []txStep{{finish, status}}
	if mode == config.ModeOracle {
		end = []txStep{{finish, 4}, {testAutocommitOn, status}}
	}
	if !serveTxSteps(ctx, t, p, end) || !servePrepareStatus(ctx, t, p, sessionStabilityUpdate, id+1, 1, nil, status) ||
		!testpeer.ReadCommand(ctx, t, p, optimizationExecute(id+1, value, false)) ||
		!testpeer.SendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'x'}) ||
		!testpeer.ReadCommand(ctx, t, p, stmtPacket(25, id+1)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
		return false
	}
	return true
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

func TestPlatformTransportUnixMixedSessions(t *testing.T) {
	// Subtest-derived TempDir names can exceed sockaddr_un's platform limit.
	directory := testpeer.UnixTempDir(t)
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				path := filepath.Join(directory, "mixed.sock")
				listener, err := net.Listen(networkUnix, path)
				if err != nil {
					t.Fatal(err)
				}
				runPlatformMixedSession(t, listener, config.Config{Network: networkUnix, Addr: path, Mode: mode}, framing)
			})
		}
	}
}

func TestProxyUserChangeUser(t *testing.T) {
	for _, framing := range []string{testStandard, testCompression, testOB20} {
		for _, charset := range []string{testUTF8MB4, localFilenameCharset} {
			t.Run(framing+"/"+charset, func(t *testing.T) { testProxyUserChangeUser(t, framing, charset) })
		}
	}
}

func testProxyUserChangeUser(t *testing.T, framing, charset string) {
	t.Helper()
	cfg := config.Config{User: testUser, ProxyUser: testProxyUser, Mode: config.ModeOracle, Session: config.SessionOptions{Charset: charset},
		Protocol: config.ProtocolOptions{Compression: config.FeatureOff, OB20: config.FeatureOff, Checksum: config.FeatureOff, NewMetadata: config.FeatureOff, ProxyChecksum: config.FeatureOff, FullLinkTrace: config.FeatureOff}}
	if framing == testCompression {
		cfg.Protocol.Compression = config.FeatureRequired
	}
	if framing == testOB20 {
		cfg.Protocol.OB20 = config.FeatureRequired
	}
	cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolGreeting(framing == testCompression)) || !testpeer.ReadAuthResponse(ctx, t, p) {
			return
		}
		var variables map[string]string
		if framing == testOB20 {
			variables = map[string]string{testProxyCapability: "256"}
		}
		if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(6, variables)) {
			return
		}
		var err error
		if framing == testCompression {
			err = p.EnableCompression()
		}
		if framing == testOB20 {
			err = p.EnableOB20(wire.OB20Options{ConnectionID: 42})
		}
		if err != nil {
			t.Error(err)
			return
		}
		if !serveAttributeBaseline(ctx, t, p, 6, charset) {
			return
		}
		ctx = context.Background()
		for _, proxy := range []string{"测试", ""} {
			attrs := map[string]string{testDriverDialectAttribute: testDriverDialectValue, testClientAttributeCapability: "1", testProxyRequestCapability: "1048576"}
			if proxy != "" {
				attrs[testProxyUserAttribute] = proxy
				if charset == localFilenameCharset {
					attrs[testProxyUserAttribute] = testAttributeGBKValue
				}
			}
			if framing == testOB20 {
				attrs[testProxyRequestCapability] = "1081600"
			}
			encoded, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs, Attributes: attrs})
			if err != nil {
				t.Error(err)
				return
			}
			want := append(changeUserPacket(t, config.ModeOracle, ""), encoded[34:]...)
			if !testpeer.ServeOK(ctx, t, p, want, 6) || !serveAttributeBaseline(ctx, t, p, 6, charset) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	created, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(created)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	for _, proxy := range []string{"测试", ""} {
		if err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName, Password: changeUserPassword, ProxyUser: proxy}, ""); err != nil {
			t.Fatal("explicit proxy identity change rejected", err)
		}
		if err = pinned.PingContext(ctx); err != nil {
			t.Fatal("proxy change left unread response", err)
		}
	}
	if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
		t.Fatal("changed proxy identity returned to pool", err)
	}
}

const testProxyUserAttribute = "__ob_client_proxy_user_name"

func TestVersionProbeSessions(t *testing.T) {
	for _, skip := range []bool{false, true} {
		for _, status := range []byte{2, 6} {
			for _, framing := range []string{testStandard, testCompression, testOB20} {
				t.Run(fmt.Sprintf("%t/%d/%s", skip, status, framing), func(t *testing.T) {
					var dials atomic.Int32
					peer := testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
						ctx := context.Background()
						if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, framing, status) || !serveVersionProbeBaseline(ctx, t, p, status, skip) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
					})
					cfg := config.Config{User: testUser, Session: config.SessionOptions{SkipVersionProbe: skip}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
						dials.Add(1)
						return peer(ctx, network, addr)
					}}
					if framing == testCompression {
						cfg.Protocol.Compression = config.FeatureRequired
					}
					if framing == testOB20 {
						cfg.Protocol.OB20 = config.FeatureRequired
					}
					created, err := oceanbase.NewConnector(cfg)
					if err != nil {
						t.Fatal(err)
					}
					cfg.Session.SkipVersionProbe = !skip
					db := sql.OpenDB(created)
					defer func() { _ = db.Close() }()
					for range 2 {
						pinned, err := db.Conn(context.Background())
						if err != nil {
							t.Fatal(err)
						}
						if err = pinned.PingContext(context.Background()); err != nil {
							t.Error(err)
						}
						if err = pinned.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if dials.Load() != 1 {
						t.Fatal("version policy broke pool reuse", dials.Load())
					}
				})
			}
		}
	}
}

func checkMixedPoolIdle(t testing.TB, stats sql.DBStats) {
	t.Helper()
	if stats.OpenConnections != 1 || stats.InUse != 0 || stats.Idle != 1 {
		t.Fatal("mixed session leaked a pool lease", stats)
	}
}
