package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const credentialsStage = "credentials"
const readySQL = "SET @ready=1"
const escapedValue = "x\\'y"
const tlsStage = "tls"

func peerDial(t *testing.T, serve func(context.Context, *wire.PacketIO), budget ...time.Duration) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	timeout := 3 * time.Second
	if len(budget) > 0 {
		timeout = budget[0]
	}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		if err := server.SetDeadline(time.Now().Add(timeout)); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = server.Close() }()
			serve(ctx, wire.NewPacketIO(authPeerConn{server}, 64<<20))
		}()
		t.Cleanup(func() { _ = client.Close(); _ = server.Close(); <-done })
		return client, nil
	}
}

func serveConnect(ctx context.Context, t testing.TB, p *wire.PacketIO, status byte, offers ...uint64) bool {
	t.Helper()
	greeting := authGreeting(authNative, false)
	offset := bytes.IndexByte(greeting[1:], 0) + 2 + 4 + 8 + 1
	for _, offer := range offers {
		binary.LittleEndian.PutUint16(greeting[offset:], binary.LittleEndian.Uint16(greeting[offset:])|uint16(offer))
		binary.LittleEndian.PutUint16(greeting[offset+5:], binary.LittleEndian.Uint16(greeting[offset+5:])|uint16(offer>>16))
	}
	if err := p.WriteMessage(ctx, greeting); err != nil {
		t.Error(err)
		return false
	}
	if _, err := p.ReadMessage(ctx); err != nil {
		t.Error(err)
		return false
	}
	if err := p.WriteMessage(ctx, []byte{0, 0, 0, status, 0, 0, 0}); err != nil {
		t.Error(err)
		return false
	}
	return true
}

func serveOK(ctx context.Context, t testing.TB, p *wire.PacketIO, want []byte, status byte) bool {
	t.Helper()
	p.ResetSequence()
	data, err := p.ReadMessage(ctx)
	if err != nil {
		t.Error(err)
		return false
	}
	if !bytes.Equal(data, want) {
		t.Errorf("command mismatch: got %q want %q", data, want)
		return false
	}
	if err = p.WriteMessage(ctx, []byte{0, 0, 0, status, 0, 0, 0}); err != nil {
		t.Error(err)
		return false
	}
	return true
}

func serveBaseline(ctx context.Context, t testing.TB, p *wire.PacketIO, status byte, deprecate ...bool) bool {
	return serveOK(ctx, t, p, append([]byte{3}, []byte("SET NAMES utf8mb4")...), status) && serveOK(ctx, t, p, append([]byte{3}, []byte("SET autocommit=1")...), status) && serveVersion(ctx, t, p, status, len(deprecate) > 0 && deprecate[0]) && serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status)
}

func serveVersion(ctx context.Context, t testing.TB, p *wire.PacketIO, status byte, deprecate bool) bool {
	query := "SELECT ob_version()"
	if status&4 != 0 {
		query += " FROM dual"
	}
	if !readQuery(ctx, t, p, query) {
		return false
	}
	if !sendPackets(ctx, t, p, []byte{1}, textColumn("version", 253, 45, 0)) {
		return false
	}
	if !deprecate && !sendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) {
		return false
	}
	terminal := []byte{254, 0, 0, status, 0}
	if deprecate {
		terminal = []byte{254, 0, 0, status, 0, 0, 0}
	}
	return sendPackets(ctx, t, p, []byte{7, '4', '.', '3', '.', '5', '.', '1'}, terminal)
}

func serveQuit(t testing.TB, p *wire.PacketIO) {
	t.Helper()
	data, err := p.ReadMessage(context.Background())
	if err != nil || !bytes.Equal(data, []byte{1}) {
		t.Errorf("missing quit: %x %v", data, err)
		return
	}
	if _, err = p.ReadMessage(context.Background()); err == nil {
		t.Error("missing transport close")
	}
}

func TestConnectBudget(t *testing.T) {
	for _, stage := range []string{"dial", credentialsStage, "greeting", tlsStage, "init"} {
		t.Run(stage, func(t *testing.T) {
			var deadline time.Time
			cfg := Config{User: testUser, ConnectTimeout: 70 * time.Millisecond}
			cfg.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				deadline, _ = ctx.Deadline()
				<-ctx.Done()
				return nil, ctx.Err()
			}
			if stage != "dial" {
				if stage == credentialsStage {
					cfg.Credentials = func(ctx context.Context) (Credentials, error) {
						d, _ := ctx.Deadline()
						if d != deadline {
							t.Error("credentials received new budget")
						}
						<-ctx.Done()
						return Credentials{}, ctx.Err()
					}
				}
				if stage == tlsStage {
					cfg.TLSConfig, _ = testTLSConfig(t)
				}
				dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if stage == "greeting" {
						<-ctx.Done()
						return
					}
					greeting := authGreeting(authNative, stage == tlsStage)
					if err := p.WriteMessage(ctx, greeting); err != nil {
						t.Error(err)
						return
					}
					if stage == credentialsStage {
						<-ctx.Done()
						return
					}
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					if stage == tlsStage {
						<-ctx.Done()
						return
					}
					if err := p.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
						t.Error(err)
						return
					}
					p.ResetSequence()
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					<-ctx.Done()
				})
				cfg.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) {
					deadline, _ = ctx.Deadline()
					return dial(ctx, n, a)
				}
			}
			c, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err = c.Connect(context.Background())
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stage %s escaped total budget: %v", stage, err)
			}
			if deadline.IsZero() || time.Since(start) > time.Second {
				t.Fatal("unbounded Connect")
			}
		})
	}
}

func TestConnectorModePinning(t *testing.T) {
	var attempts atomic.Int32
	c, err := NewConnector(Config{User: testUser, Session: SessionOptions{InitSQL: []string{readySQL}}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		attempt := attempts.Add(1)
		status := byte(6)
		if attempt == 1 {
			status = 2
		}
		if !serveConnect(ctx, t, p, status) || !serveBaseline(ctx, t, p, status) {
			return
		}
		p.ResetSequence()
		if data, err := p.ReadMessage(ctx); err != nil || string(data) != "\x03SET @ready=1" {
			t.Errorf("init SQL: %q %v", data, err)
			return
		}
		response := []byte{0, 0, 0, status, 0, 0, 0}
		if attempt == 1 {
			response = []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'n', 'o'}
		}
		if err := p.WriteMessage(ctx, response); err != nil {
			t.Error(err)
			return
		}
		p.ResetSequence()
		if attempt == 1 {
			if _, err := p.ReadMessage(ctx); err == nil {
				t.Error("failed init kept connection")
			}
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) || !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Connect(context.Background()); err == nil {
		t.Fatal("initialization failure hidden")
	}
	// Oracle must succeed even though the first MySQL authentication succeeded.
	conn, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("failed initialization pinned mode: %v", err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectorConcurrentModes(t *testing.T) {
	var mu sync.Mutex
	next := byte(2)
	initialized := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	c, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		mu.Lock()
		status := next
		next = 6
		mu.Unlock()
		if !serveConnect(ctx, t, p, status) {
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err != nil {
			t.Error(err)
			return
		}
		initialized <- struct{}{}
		<-release
		if err := p.WriteMessage(ctx, []byte{0, 0, 0, status, 0, 0, 0}); err != nil {
			t.Error(err)
			return
		}
		if !serveVersion(ctx, t, p, status, false) {
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status) {
			return
		}
		p.ResetSequence()
		data, err := p.ReadMessage(context.Background())
		if err == nil {
			if !bytes.Equal(data, []byte{1}) {
				t.Error("unexpected command")
			}
			if _, err = p.ReadMessage(context.Background()); err == nil {
				t.Error("missing transport close")
			}
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			conn, err := c.Connect(context.Background())
			if err == nil {
				err = conn.Close()
			}
			results <- err
		}()
	}
	for range 2 {
		select {
		case <-initialized:
		case <-time.After(time.Second):
			t.Fatal("Connect serialized initialization or failed")
		}
	}
	releaseOnce.Do(func() { close(release) })
	passed, failed := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			passed++
		} else if errors.Is(err, ErrModeMismatch) {
			failed++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || failed != 1 {
		t.Fatalf("mixed pool modes: %d %d", passed, failed)
	}
}

func TestPingFailure(t *testing.T) {
	var pingSeen atomic.Bool
	cfg := Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		p.ResetSequence()
		if data, err := p.ReadMessage(context.Background()); err != nil || !bytes.Equal(data, []byte{14}) {
			t.Errorf("missing ping: %x %v", data, err)
			return
		}
		// Returning closes the transport before any Ping response.
		pingSeen.Store(true)
	})}
	c, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	t.Cleanup(func() { _ = db.Close() })
	if err = db.PingContext(context.Background()); err == nil {
		t.Fatal("Ping silently succeeded")
	}
	if !pingSeen.Load() {
		t.Fatal("Ping failed before sending Ping command")
	}
	if db.Stats().Idle != 0 {
		t.Fatal("failed physical connection returned to idle pool")
	}
}

func TestConnectInitializationAndPing(t *testing.T) {
	cfg := Config{User: testUser, Session: SessionOptions{Variables: map[string]string{"time_zone": testUTCOffset}, InitSQL: []string{readySQL}}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		for _, sql := range []string{"SET SESSION time_zone = '+00:00'", readySQL, "SET NAMES utf8mb4", "SET SESSION time_zone = '+00:00'"} {
			if !serveOK(ctx, t, p, append([]byte{3}, sql...), 2) {
				return
			}
		}
		if !serveOK(context.Background(), t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})}
	c, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

var _ driver.Connector = (*connector)(nil)

func TestConnectConfigValidation(t *testing.T) {
	for _, session := range []SessionOptions{
		{Variables: map[string]string{"a; SELECT 1": "value"}},
		{Variables: map[string]string{"a": "bad\x00value"}},
		{InitSQL: []string{"bad\x00SQL"}},
	} {
		if _, err := NewConnector(Config{User: testUser, Session: session}); err == nil {
			t.Fatal("invalid initialization configuration accepted")
		}
	}
}

func TestConnectSessionValueEncoding(t *testing.T) {
	for _, tc := range []struct {
		mode        Mode
		status      uint16
		value, want string
	}{
		{ModeMySQL, 2, escapedValue, "'x\\\\''y'"},
		{ModeMySQL, 514, escapedValue, "'x\\''y'"},
		{ModeOracle, 6, escapedValue, "'x\\''y'"},
	} {
		c := &conn{session: &handshakeSession{mode: tc.mode, ok: wire.OK{Status: tc.status}}}
		if value := c.quoteSessionValue(tc.value); value != tc.want {
			t.Fatalf("unsafe SET literal: %q want %q", value, tc.want)
		}
	}
}

func TestConnectIntegerSystemVariable(t *testing.T) {
	cfg := Config{User: testUser, Session: SessionOptions{Variables: map[string]string{testSessionVariable: "600"}}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) ||
			!serveOK(ctx, t, p, []byte("\x03SET SESSION wait_timeout = 600"), 2) ||
			!serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), 2) ||
			!serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) ||
			!serveOK(context.Background(), t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})}
	c, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	defer func() { _ = db.Close() }()
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConnectOceanBaseVersion(t *testing.T) {
	c, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = created.Close() }()
	if created.(*conn).session.obVersion != "4.3.5.1" {
		t.Fatal("OceanBase version was inferred from MySQL greeting instead of server query")
	}
}
