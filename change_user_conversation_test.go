package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type changeUserConversation struct {
	scriptedConversation
	credentials []Credentials
	closeErr    error
}

func (p *changeUserConversation) Respond(ctx context.Context, credentials Credentials, challenge AuthChallenge) ([]byte, error) {
	p.credentials = append(p.credentials, credentials)
	return p.scriptedConversation.Respond(ctx, credentials, challenge)
}

func (p *changeUserConversation) Close() error {
	_ = p.scriptedConversation.Close()
	return p.closeErr
}

func TestChangeUserConversation(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, path := range []string{authNilOutputPath, authEmptyOutputPath, "token-output", authTLSDeniedPath, serverErrorPath, rejectExtraTokenPath, requiresFinalOKPath, authCloseErrorPath} {
			t.Run(fmt.Sprintf("%d/%s", mode, path), func(t *testing.T) {
				testChangeUserConversation(t, mode, path)
			})
		}
	}
}

func testChangeUserConversation(t *testing.T, mode Mode, path string) {
	t.Helper()
	status := byte(2)
	if mode == ModeOracle {
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
	credentials := [2]Credentials{
		{User: changeUserName, Password: changeUserPassword},
		{User: changeUserName, Password: changeUserPassword + "-rotated"},
	}
	var providers, dialed atomic.Int32
	created := 0
	cfg := Config{Mode: mode, Credentials: func(context.Context) (Credentials, error) {
		providers.Add(1)
		return Credentials{User: testUser, Password: testPassword}, nil
	}, Auth: AuthOptions{Plugins: map[string]AuthFactory{testGSSPlugin: func() AuthPlugin {
		if created >= len(plugins) {
			t.Error("extra authentication plugin use")
			return nil
		}
		p := plugins[created]
		created++
		return p
	}}}}
	peerDone := make(chan struct{})
	dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		defer close(peerDone)
		serveChangeUserConversation(ctx, t, p, mode, status, seed, plugins, path)
	})
	cfg.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed.Add(1)
		return dial(ctx, network, address)
	}
	connector, err := NewConnector(cfg)
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
	var physical *conn
	if err = pinned.Raw(func(raw any) error { physical = raw.(*conn); return nil }); err != nil {
		t.Fatal(err)
	}
	for i, credentials := range credentials {
		err = ChangeUser(ctx, pinned, credentials, "")
		checkChangeUserConversation(ctx, t, pinned, physical, plugins[i], credentials, seed, mode, path, i == 1, err)
	}
	if created != 2 || providers.Load() != 1 || dialed.Load() != 1 {
		t.Fatalf("created=%d credential requests=%d dials=%d", created, providers.Load(), dialed.Load())
	}
	if err = pinned.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) || db.Stats().OpenConnections != 0 || !physical.closed.Load() {
		t.Fatalf("changed identity returned to pool: %v %+v", err, db.Stats())
	}
	select {
	case <-peerDone:
	case <-ctx.Done():
		t.Fatal("authentication peer did not finish", ctx.Err())
	}
}

func serveChangeUserConversation(ctx context.Context, t *testing.T, p *wire.PacketIO, mode Mode, status byte, seed []byte, plugins [2]*changeUserConversation, path string) {
	t.Helper()
	if !serveConnect(ctx, t, p, status) || !serveBaseline(ctx, t, p, status) {
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
			if mode == ModeOracle {
				packet = append(packet, "next"...)
			}
			packet = append(packet, 0)
			packet = append(packet, testGSSPlugin...)
			packet = append(packet, 0)
		}
		if !readCommand(ctx, t, p, packet) {
			return
		}
		if i == 0 {
			switchPacket := append(append([]byte{0xfe}, testGSSPlugin...), 0)
			if !sendPackets(ctx, t, p, append(switchPacket, seed...)) {
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
		if !serveBaseline(ctx, t, p, status) || !serveOK(ctx, t, p, []byte{14}, status) {
			return
		}
	}
	p.ResetSequence()
	serveQuit(t, p)
}

func serveChangeUserConversationEnd(ctx context.Context, t *testing.T, p *wire.PacketIO, plugin *changeUserConversation, last bool, status byte, path string) bool {
	t.Helper()
	if !sendPackets(ctx, t, p, plugin.token) {
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
		if !sendPackets(ctx, t, p, final) {
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

func checkChangeUserConversation(ctx context.Context, t *testing.T, pinned *sql.Conn, physical *conn, plugin *changeUserConversation, credentials Credentials, seed []byte, mode Mode, path string, last bool, err error) {
	t.Helper()
	wantFailure := last && conversationFailure(path)
	if (err != nil) != wantFailure {
		t.Fatalf("change failed=%v: %v", wantFailure, err)
	}
	if last && path == authTLSDeniedPath && !errors.Is(err, ErrTLSRequired) ||
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
		if !physical.closed.Load() || pinned.PingContext(ctx) == nil {
			t.Fatal("failed authentication retained a usable physical connection")
		}
		return
	}
	if physical.cfg.Password != "" || physical.cfg.Credentials != nil || physical.session.user != credentials.User ||
		physical.session.mode != mode || physical.session.authPlugin != testGSSPlugin || !bytes.Equal(physical.session.authChallenge, seed) {
		t.Fatal("changed identity retained credentials or lost authentication state")
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func conversationFailure(path string) bool {
	switch path {
	case authNilOutputPath, authEmptyOutputPath, "token-output":
		return false
	default:
		return true
	}
}
