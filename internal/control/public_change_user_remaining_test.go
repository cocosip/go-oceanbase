package control_test

import (
	bytes "bytes"
	context "context"
	sql "database/sql"
	errors "errors"
	fmt "fmt"
	io "io"
	net "net"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestChangeUserIsolation(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, prepare := range []config.PrepareMode{config.PrepareServer, config.PrepareClient} {
			t.Run(fmt.Sprintf("%d/%d", mode, prepare), func(t *testing.T) { testChangeUserIsolation(t, mode, prepare) })
		}
	}
}

func testChangeUserIsolation(t *testing.T, mode config.Mode, prepare config.PrepareMode) {
	t.Helper()
	status := byte(2)
	if mode == config.ModeOracle {
		status = 6
	}
	cfg := config.Config{User: testUser, Mode: mode, PrepareMode: prepare, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		serveChangeUserIsolation(ctx, t, p, mode, prepare, status)
	})}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	old, err := pinned.PrepareContext(ctx, testBatchSQL)
	if err != nil {
		t.Fatal(err)
	}
	target, err := oceanbase.QueryTarget(ctx, pinned)
	if err != nil {
		t.Fatal(err)
	}
	if err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, ""); err != nil {
		t.Fatal(err)
	}
	if err = oceanbase.CancelQuery(ctx, connector, target); !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("old cancel target survived identity change")
	}
	if mode == config.ModeMySQL {
		if got, err := oceanbase.ReadWarnings(ctx, pinned); err != nil || len(got) != 0 {
			t.Fatalf("identity change exposed old warning details: %+v %v", got, err)
		}
	}
	if _, err = old.ExecContext(ctx, 1); !errors.Is(err, oceanbase.ErrResource) {
		t.Fatalf("old statement survived identity change: %v", err)
	}
	fresh, err := pinned.PrepareContext(ctx, testBatchSQL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.ExecContext(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	if err = fresh.Close(); err != nil {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
		t.Fatalf("changed identity returned to pool: %v %+v", err, db.Stats())
	}
}

func serveChangeUserIsolation(ctx context.Context, t *testing.T, p *wire.PacketIO, mode config.Mode, prepare config.PrepareMode, status byte) {
	t.Helper()

	if !testpeer.ServeConnect(ctx, t, p, status) || !testpeer.ServeBaseline(ctx, t, p, status) {
		return
	}
	ctx = context.Background()
	if prepare == config.PrepareServer && !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) {
		return
	}
	if !testpeer.ReadCommand(ctx, t, p, changeUserPacket(t, mode, "")) || !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, status)) || !testpeer.ServeBaseline(ctx, t, p, status) {
		return
	}
	if prepare == config.PrepareServer {
		if !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) || !testpeer.ServeOK(ctx, t, p, optimizationExecute(7, 1, false), status) || !testpeer.ReadCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
	} else if !testpeer.ServeOK(ctx, t, p, []byte("\x03INSERT INTO t VALUES ( 1 )"), status) {
		return
	}
	if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func TestChangeUserConversation(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, path := range []string{authNilOutputPath, authEmptyOutputPath, "token-output", authTLSDeniedPath, serverErrorPath, rejectExtraTokenPath, requiresFinalOKPath, authCloseErrorPath} {
			t.Run(fmt.Sprintf("%d/%s", mode, path), func(t *testing.T) {
				testChangeUserConversation(t, mode, path)
			})
		}
	}
}

const authNilOutputPath = "nil-output"

const authEmptyOutputPath = "empty-output"

const rejectExtraTokenPath = "reject-extra-token"

func testChangeUserConversation(t *testing.T, mode config.Mode, path string) {
	t.Helper()
	status := byte(2)
	if mode == config.ModeOracle {
		status = 6
	}
	seed := []byte("service/db.example\x00Kerberos\x00")
	plugins := [2]*changeUserConversation{}
	for i := range plugins {
		plugins[i] = &changeUserConversation{scriptedConversation: scriptedConversation{
			seed: seed, token: []byte{0xff, 11, 12}, output: []byte("final-token"),
		}}
	}
	second := plugins[1]
	switch path {
	case authNilOutputPath:
		second.output = nil
		second.token = commandPacket(0, 0, status) // An OK-shaped token is still conversation data.
	case authEmptyOutputPath:
		second.output = []byte{}
		second.token = []byte{0xfe, 11, 12}
	case authTLSDeniedPath:
		second.moreTLS = true
	case authCloseErrorPath:
		second.closeErr = errors.New("conversation cleanup failed")
	}
	credentials := [2]config.Credentials{
		{User: changeUserName, Password: changeUserPassword},
		{User: changeUserName, Password: changeUserPassword + "-rotated"},
	}
	var providers, dialed atomic.Int32
	created := 0
	cfg := config.Config{Mode: mode, Credentials: func(context.Context) (config.Credentials, error) {
		providers.Add(1)
		return config.Credentials{User: testUser, Password: testPassword}, nil
	}, Auth: config.AuthOptions{Plugins: map[string]config.AuthFactory{testGSSPlugin: func() config.AuthPlugin {
		if created >= len(plugins) {
			t.Error("extra authentication plugin use")
			return nil
		}
		p := plugins[created]
		created++
		return p
	}}}}
	peerDone := make(chan struct{})
	dial := testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		defer close(peerDone)
		serveChangeUserConversation(ctx, t, p, mode, status, seed, plugins, path)
	})
	cfg.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed.Add(1)
		return dial(ctx, network, address)
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	for i, credentials := range credentials {
		err = oceanbase.ChangeUser(ctx, pinned, credentials, "")
		checkChangeUserConversation(ctx, t, pinned, plugins[i], credentials, seed, mode, path, i == 1, err)
	}
	if created != 2 || providers.Load() != 1 || dialed.Load() != 1 {
		t.Fatalf("created=%d credential requests=%d dials=%d", created, providers.Load(), dialed.Load())
	}
	if err = pinned.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) || db.Stats().OpenConnections != 0 {
		t.Fatalf("changed identity returned to pool: %v %+v", err, db.Stats())
	}
	select {
	case <-peerDone:
	case <-ctx.Done():
		t.Fatal("authentication peer did not finish", ctx.Err())
	}
}

type changeUserConversation struct {
	scriptedConversation
	credentials []config.Credentials
	closeErr    error
}

func (p *changeUserConversation) Close() error {
	_ = p.scriptedConversation.Close()
	return p.closeErr
}

type scriptedConversation struct {
	seed, token []byte
	output      []byte
	calls       int
	closed      int
	moreTLS     bool
}

func (p *scriptedConversation) Close() error { p.closed++; return nil }

func (p *scriptedConversation) Complete() bool { return p.calls == 2 }

func (p *scriptedConversation) Respond(_ context.Context, _ config.Credentials, c config.AuthChallenge) ([]byte, error) {
	p.calls++
	if p.calls == 1 && !c.More && bytes.Equal(c.Nonce, p.seed) {
		return []byte("initial-token"), nil
	}
	if p.calls == 2 && c.More && bytes.Equal(c.Data, p.token) {
		return p.output, nil
	}
	return nil, wire.ErrMalformed
}

func (p *scriptedConversation) Security(c config.AuthChallenge) config.AuthSecurity {
	if c.More && p.moreTLS {
		return config.AuthRequireTLS
	}
	return config.AuthChallengeResponse
}

func (p *changeUserConversation) Respond(ctx context.Context, credentials config.Credentials, challenge config.AuthChallenge) ([]byte, error) {
	p.credentials = append(p.credentials, credentials)
	return p.scriptedConversation.Respond(ctx, credentials, challenge)
}

func serveChangeUserConversation(ctx context.Context, t *testing.T, p *wire.PacketIO, mode config.Mode, status byte, seed []byte, plugins [2]*changeUserConversation, path string) {
	t.Helper()
	if !testpeer.ServeConnect(ctx, t, p, status) || !testpeer.ServeBaseline(ctx, t, p, status) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i, plugin := range plugins {
		packet := changeUserPacket(t, mode, "")
		if i == 1 {
			packet = append([]byte{17}, changeUserName...)
			packet = append(packet, 0, byte(len("initial-token")))
			packet = append(packet, "initial-token"...)
			if mode == config.ModeOracle {
				packet = append(packet, "next"...)
			}
			packet = append(packet, 0)
			packet = append(packet, testGSSPlugin...)
			packet = append(packet, 0)
		}
		if !testpeer.ReadCommand(ctx, t, p, packet) {
			return
		}
		if i == 0 {
			switchPacket := append(append([]byte{0xfe}, testGSSPlugin...), 0)
			if !testpeer.SendPackets(ctx, t, p, append(switchPacket, seed...)) {
				return
			}
			if got, err := p.ReadMessage(ctx); err != nil || string(got) != "initial-token" {
				t.Errorf("missing initial conversation response: %x %v", got, err)
				return
			}
		}
		if !serveChangeUserConversationEnd(ctx, t, p, plugin, i == 1, status, path) {
			return
		}
		if !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
			return
		}
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

const testGSSPlugin = "auth_gssapi_client"

func serveChangeUserConversationEnd(ctx context.Context, t *testing.T, p *wire.PacketIO, plugin *changeUserConversation, last bool, status byte, path string) bool {
	t.Helper()
	if !testpeer.SendPackets(ctx, t, p, plugin.token) {
		return false
	}
	if !plugin.moreTLS && plugin.output != nil {
		if got, err := p.ReadMessage(ctx); err != nil || !bytes.Equal(got, plugin.output) {
			t.Errorf("conversation response: %x %v", got, err)
			return false
		}
	}
	if last && path == requiresFinalOKPath {
		return false // Transport EOF before final OK must fail authentication.
	}
	if !plugin.moreTLS {
		final := commandPacket(0, 0, status)
		if last && path == serverErrorPath {
			final = []byte{0xff, 0x15, 0x04, '#', '2', '8', '0', '0', '0', 'x'}
		}
		if last && path == rejectExtraTokenPath {
			final = []byte{1, 4}
		}
		if !testpeer.SendPackets(ctx, t, p, final) {
			return false
		}
	}
	if last && conversationFailure(path) {
		if got, err := p.ReadMessage(ctx); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("failed change did not close transport: %x %v", got, err)
		}
		return false
	}
	return true
}

func conversationFailure(path string) bool {
	switch path {
	case authNilOutputPath, authEmptyOutputPath, "token-output":
		return false
	default:
		return true
	}
}

const requiresFinalOKPath = "requires-final-ok"

const testPassword = "secret"

func checkChangeUserConversation(ctx context.Context, t *testing.T, pinned *sql.Conn, plugin *changeUserConversation, credentials config.Credentials, _ []byte, _ config.Mode, path string, last bool, err error) {
	t.Helper()
	wantFailure := last && conversationFailure(path)
	if (err != nil) != wantFailure {
		t.Fatalf("change failed=%v: %v", wantFailure, err)
	}
	if last && path == authTLSDeniedPath && !errors.Is(err, dberrors.ErrTLSRequired) ||
		last && path == authCloseErrorPath && !errors.Is(err, plugin.closeErr) {
		t.Fatalf("change lost authentication error: %v", err)
	}
	wantCalls := 2
	if last && path == authTLSDeniedPath {
		wantCalls = 1
	}
	if plugin.calls != wantCalls || plugin.closed != 1 || len(plugin.credentials) != wantCalls {
		t.Fatalf("plugin: calls=%d closed=%d credential calls=%d", plugin.calls, plugin.closed, len(plugin.credentials))
	}
	for _, got := range plugin.credentials {
		if got != credentials {
			t.Fatal("authentication used credentials from another identity")
		}
	}
	if wantFailure {
		if pinned.PingContext(ctx) == nil {
			t.Fatal("failed authentication retained a usable physical connection")
		}
		return
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

const authCloseErrorPath = "close-error"
