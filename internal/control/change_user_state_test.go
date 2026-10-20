package control

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/lifetime"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

const authNilOutputPath = "nil-output"

const authEmptyOutputPath = "empty-output"

const rejectExtraTokenPath = "reject-extra-token"

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
			packet = append([]byte{17}, stateUserName...)
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

const statePassword = "secret"

const authCloseErrorPath = "close-error"

const serverErrorPath = "server-error"

func commandPacket(affected, id uint64, status byte) []byte {
	data := binary.LittleEndian.AppendUint64([]byte{254}, affected)
	data = binary.LittleEndian.AppendUint64(append(data, 254), id)
	return append(append([]byte{0}, data...), status, 0, 0, 0)
}

const stateUserName = "next@tenant"

const stateUserPassword = "next-secret"

func changeUserPacket(t *testing.T, mode config.Mode, database string) []byte {
	t.Helper()
	response, err := auth.Native(stateUserPassword, []byte("12345678901234567890"))
	if err != nil {
		t.Fatal(err)
	}
	if mode == config.ModeOracle && database == "" {
		database = "next"
	}
	b := append([]byte{17}, stateUserName...)
	b = append(b, 0, byte(len(response)))
	b = append(b, response...)
	b = append(b, database...)
	b = append(b, 0)
	b = append(b, authentication.Native...)
	return append(b, 0)
}

const authTLSDeniedPath = "tls-denied"

func TestChangeUserConversationState(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, path := range []string{authNilOutputPath, authEmptyOutputPath, "token-output", authTLSDeniedPath, serverErrorPath, rejectExtraTokenPath, requiresFinalOKPath, authCloseErrorPath} {
			t.Run(fmt.Sprintf("%d/%s", mode, path), func(t *testing.T) {
				testChangeUserConversationState(t, mode, path)
			})
		}
	}
}

func testChangeUserConversationState(t *testing.T, mode config.Mode, path string) {
	t.Helper()
	status := byte(2)
	if mode == config.ModeOracle {
		status = 6
	}
	seed := []byte("service/db.example\x00Kerberos\x00")
	plugins := [2]*changeUserConversation{}
	for i := range plugins {
		plugins[i] = &changeUserConversation{scriptedConversation: scriptedConversation{seed: seed, token: []byte{0xff, 11, 12}, output: []byte("final-token")}}
	}
	second := plugins[1]
	switch path {
	case authNilOutputPath:
		second.output = nil
		second.token = commandPacket(0, 0, status)
	case authEmptyOutputPath:
		second.output = []byte{}
		second.token = []byte{0xfe, 11, 12}
	case authTLSDeniedPath:
		second.moreTLS = true
	case authCloseErrorPath:
		second.closeErr = errors.New("conversation cleanup failed")
	}
	var providers atomic.Int32
	created := 0
	peerDone := make(chan struct{})
	cfg := config.Config{Mode: mode, Credentials: func(context.Context) (config.Credentials, error) {
		providers.Add(1)
		return config.Credentials{User: testUser, Password: statePassword}, nil
	}, Auth: config.AuthOptions{Plugins: map[string]config.AuthFactory{testGSSPlugin: func() config.AuthPlugin {
		if created >= 2 {
			t.Error("extra authentication plugin")
			return nil
		}
		p := plugins[created]
		created++
		return p
	}}}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		defer close(peerDone)
		serveChangeUserConversation(ctx, t, p, mode, status, seed, plugins, path)
	})}
	configured, err := controlConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := configured.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Discard()
	op, err := s.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	m := New()
	cache, resolver, cursors, lobs := &statement.Cache{}, &catalog.Resolver{}, &cursor.Manager{}, &lob.Manager{}
	for i, credentials := range []config.Credentials{{User: stateUserName, Password: stateUserPassword}, {User: stateUserName, Password: stateUserPassword + "-rotated"}} {
		err = m.ChangeUser(ctx, op, cache, resolver, cursors, lobs, credentials, "", false)
		failure := i == 1 && conversationFailure(path)
		if (err != nil) != failure {
			t.Fatalf("failure=%v: %v", failure, err)
		}
		if failure {
			if !s.Closed() {
				t.Fatal("failed authentication retained physical session")
			}
			continue
		}
		state, identity := op.Config(), op.Protocol()
		if state.Password != "" || state.Credentials != nil || identity.User != credentials.User || identity.Mode != mode || identity.AuthPlugin != testGSSPlugin || !bytes.Equal(identity.AuthChallenge, seed) {
			t.Fatal("changed identity retained credentials or lost authentication state")
		}
		if _, err = command.ExecOK(ctx, op, []byte{14}); err != nil {
			t.Fatal(err)
		}
	}
	if created != 2 || providers.Load() != 1 {
		t.Fatalf("plugins=%d providers=%d", created, providers.Load())
	}
	if err = closeControlSession(ctx, s, op); err != nil {
		t.Fatal(err)
	}
	if !s.Closed() {
		t.Fatal("changed session transport remained open after close")
	}
	select {
	case <-peerDone:
	case <-ctx.Done():
		t.Fatal("authentication peer did not finish", ctx.Err())
	}
}

func TestChangeUserResourceIsolation(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, prepare := range []config.PrepareMode{config.PrepareServer, config.PrepareClient} {
			t.Run(fmt.Sprintf("%d/%d", mode, prepare), func(t *testing.T) {
				status := byte(2)
				if mode == config.ModeOracle {
					status = 6
				}
				configured, err := controlConnector(config.Config{User: testUser, Mode: mode, PrepareMode: prepare, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.ServeConnect(ctx, t, p, status) || !testpeer.ServeBaseline(ctx, t, p, status) {
						return
					}
					ctx = context.Background()
					if !testpeer.ReadCommand(ctx, t, p, changeUserPacket(t, mode, "")) || !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, status)) || !testpeer.ServeBaseline(ctx, t, p, status) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})})
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				s, err := configured.Connect(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Discard()
				op, err := s.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer op.Release()
				m := New()
				cache, resolver, cursors, lobs := &statement.Cache{}, &catalog.Resolver{}, &cursor.Manager{}, &lob.Manager{}
				old := statement.Adopt(op, 0, sqltext.Plan{}, nil)
				refs := make([]session.ResourceRef, 2)
				for i, kind := range []session.ResourceKind{session.LOBResource, session.CursorResource} {
					refs[i], err = op.Register(kind)
					if err != nil {
						t.Fatal(err)
					}
					if err = op.Publish(refs[i]); err != nil {
						t.Fatal(err)
					}
				}
				target := op.ArmTarget()
				if err = m.ChangeUser(ctx, op, cache, resolver, cursors, lobs, config.Credentials{User: stateUserName, Password: stateUserPassword}, "", false); err != nil {
					t.Fatal(err)
				}
				if _, err = lobs.Execute(ctx, op, refs[0], lob.Size, 0, 0, nil, "", false); !errors.Is(err, dberrors.ErrResource) {
					t.Fatal("old locator survived", err)
				}
				if _, _, err = cursors.Fetch(ctx, op, resolver, refs[1], cursor.Position{Origin: cursor.Next}, 1, nil); !errors.Is(err, dberrors.ErrResource) {
					t.Fatal("old cursor survived", err)
				}
				if err = old.CheckOpen(op); !errors.Is(err, dberrors.ErrResource) {
					t.Fatal("old statement survived", err)
				}
				for _, ref := range refs {
					if err = lifetime.Close(ctx, op, cursors, lobs, ref); err != nil {
						t.Fatal(err)
					}
				}
				if op.BeginCancellation() {
					t.Fatal("old target armed after identity change")
				}
				if _, err = target.Begin(func() {}); !errors.Is(err, dberrors.ErrResource) {
					t.Fatal("old cancel target survived", err)
				}
				if err = closeControlSession(ctx, s, op); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
