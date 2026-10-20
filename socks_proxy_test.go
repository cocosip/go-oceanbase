package oceanbase

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
	"golang.org/x/net/proxy"
)

const socksTarget = "oceanbase.invalid:2881"
const socksUsername = "proxy-app"
const socksPassword = "proxy-secret"

func TestSOCKSDialContextSessions(t *testing.T) {
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1")
	t.Setenv("NO_PROXY", ".invalid")
	for _, status := range []byte{2, 6} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", status, framing), func(t *testing.T) {
				address := startSOCKSPeers(t, func(raw net.Conn) {
					if !serveSOCKSConnect(t, raw, socksTarget, true) {
						return
					}
					ctx := context.Background()
					p := wire.NewPacketIO(authPeerConn{Conn: raw}, 64<<20)
					if !serveCreateDatabaseAuth(ctx, t, p, framing, status) || !serveBaseline(ctx, t, p, status) || !serveOK(ctx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				dial, err := socksExampleDialer(address, &proxy.Auth{User: socksUsername, Password: socksPassword})
				if err != nil {
					t.Fatal(err)
				}
				cfg := Config{User: testUser, Addr: socksTarget, DialContext: dial}
				if framing == testCompression {
					cfg.Protocol.Compression = FeatureRequired
				}
				if framing == testOB20 {
					cfg.Protocol.OB20 = FeatureRequired
				}
				connector, err := NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				defer func() { _ = db.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err = db.PingContext(ctx); err != nil {
					t.Fatal(err)
				}
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestSOCKSDialContextRouting(t *testing.T) {
	const next = "next.invalid:2882"
	address := startSOCKSPeers(t, func(raw net.Conn) {
		if !serveSOCKSConnect(t, raw, socksTarget, false) {
			return
		}
		// Closing before the greeting is a transport failure, so the next selected target is tried.
	}, func(raw net.Conn) {
		if !serveSOCKSConnect(t, raw, next, false) {
			return
		}
		ctx := context.Background()
		p := wire.NewPacketIO(authPeerConn{Conn: raw}, 64<<20)
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	dial, err := socksExampleDialer(address, nil)
	if err != nil {
		t.Fatal(err)
	}
	connector, err := NewConnector(Config{User: testUser, DialContext: dial, Routing: RoutingOptions{
		Endpoints: []Endpoint{{Addr: socksTarget}, {Addr: next}}, Strategy: BalanceOrdered, MaxAttempts: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("SOCKS lost selected endpoint or failed to retry", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSOCKSDialContextCancellation(t *testing.T) {
	for _, budget := range []bool{false, true} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			started := make(chan struct{})
			address := startSOCKSPeers(t, func(raw net.Conn) {
				var request [3]byte
				if _, err := io.ReadFull(raw, request[:]); err != nil {
					t.Error(err)
					return
				}
				close(started)
				if _, err := raw.Read(request[:]); !errors.Is(err, io.EOF) {
					t.Error("canceled SOCKS connection retained", err)
				}
			})
			dial, err := socksExampleDialer(address, nil)
			if err != nil {
				t.Fatal(err)
			}
			const connectTimeout = 100 * time.Millisecond
			connector, err := NewConnector(Config{User: testUser, DialContext: dial, ConnectTimeout: connectTimeout})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			startedAt := time.Now()
			go func() { _, e := connector.Connect(ctx); done <- e }()
			select {
			case <-started:
			case err = <-done:
				t.Fatal("did not reach proxy negotiation", err)
			case <-time.After(2 * time.Second):
				t.Fatal("SOCKS dial stuck")
			}
			want := context.DeadlineExceeded
			if !budget {
				cancel()
				want = context.Canceled
			}
			select {
			case err = <-done:
				var timeout net.Error
				// Socket deadlines can fire before the context timer publishes its error.
				budgetExpired := budget && errors.As(err, &timeout) && timeout.Timeout() && time.Since(startedAt) >= connectTimeout
				if !errors.Is(err, want) && !budgetExpired {
					t.Fatal("proxy negotiation ignored context", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("SOCKS cancellation stuck")
			}
		})
	}
}

func TestSOCKSDialContextTLSGate(t *testing.T) {
	address := startSOCKSPeers(t, func(raw net.Conn) {
		if !serveSOCKSConnect(t, raw, socksTarget, false) {
			return
		}
		ctx := context.Background()
		p := wire.NewPacketIO(authPeerConn{Conn: raw}, 64<<20)
		if !sendPackets(ctx, t, p, authGreeting("mysql_clear_password", false)) {
			return
		}
		if data, err := p.ReadMessage(ctx); err == nil || len(data) != 0 {
			t.Error("SOCKS proxy bypassed clear-password TLS policy")
		}
	})
	dial, err := socksExampleDialer(address, nil)
	if err != nil {
		t.Fatal(err)
	}
	connector, err := NewConnector(Config{User: testUser, Password: testPassword, Addr: socksTarget, DialContext: dial})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connector.Connect(context.Background()); !errors.Is(err, ErrTLSRequired) {
		t.Fatal(err)
	}
}

func TestSOCKSDialContextVerifiedTLS(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprint(trusted), func(t *testing.T) {
			client, server := testTLSConfig(t)
			client.ServerName = ""
			target := "wrong.invalid:2881"
			if trusted {
				target = testServerName + ":2881"
			}
			var verified atomic.Bool
			client.VerifyConnection = func(state tls.ConnectionState) error {
				verified.Store(state.ServerName == testServerName && len(state.VerifiedChains) != 0)
				return nil
			}
			address := startSOCKSPeers(t, func(raw net.Conn) {
				if !serveSOCKSConnect(t, raw, target, false) {
					return
				}
				ctx := context.Background()
				p := wire.NewPacketIO(authPeerConn{Conn: raw}, 64<<20)
				if !sendPackets(ctx, t, p, authGreeting("mysql_clear_password", true)) || !readAuthResponse(ctx, t, p) {
					return
				}
				secure := tls.Server(raw, server)
				if err := secure.HandshakeContext(ctx); err != nil {
					if trusted {
						t.Error(err)
					}
					return
				}
				if err := p.ReplaceConn(authPeerConn{Conn: secure}); err != nil {
					t.Error(err)
					return
				}
				if !trusted {
					if data, err := p.ReadMessage(ctx); err == nil || len(data) != 0 {
						t.Error("untrusted SOCKS target received DB credentials")
					}
					return
				}
				data, err := p.ReadMessage(ctx)
				if err != nil || !bytes.Contains(data, []byte(testPassword+"\x00")) {
					t.Error("verified tunnel did not carry DB authentication", err)
					return
				}
				if !sendPackets(ctx, t, p, protocolOK(2, nil)) || !serveBaseline(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			dial, err := socksExampleDialer(address, nil)
			if err != nil {
				t.Fatal(err)
			}
			connector, err := NewConnector(Config{User: testUser, Password: testPassword, Addr: target, TLSConfig: client, DialContext: dial})
			if err != nil {
				t.Fatal(err)
			}
			connected, err := connector.Connect(context.Background())
			if !trusted {
				if connected != nil || err == nil || verified.Load() {
					t.Fatal("SOCKS target identity was not checked", err)
				}
				return
			}
			if err != nil || !verified.Load() {
				t.Fatal("TLS used proxy identity instead of target", err)
			}
			if err = connected.(*conn).Ping(context.Background()); err != nil {
				t.Error(err)
			}
			if err = connected.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSOCKSDialContextFailure(t *testing.T) {
	for _, failure := range []string{"method", "version", "connect-refusal", "authentication"} {
		t.Run(failure, func(t *testing.T) {
			authentication := failure == "authentication"
			address := startSOCKSPeers(t, func(raw net.Conn) {
				greeting := []byte{5, 1, 0}
				if authentication {
					greeting = []byte{5, 2, 0, 2}
				}
				if !socksRead(t, raw, greeting) {
					return
				}
				reply := []byte{5, 255}
				if failure == "version" {
					reply = []byte{4, 0}
				}
				if failure == "connect-refusal" {
					request := append([]byte{5, 1, 0, 3, 17}, "oceanbase.invalid"...)
					request = append(request, 11, 65)
					if !socksWrite(t, raw, []byte{5, 0}) || !socksRead(t, raw, request) {
						return
					}
					reply = []byte{5, 5, 0, 1, 127, 0, 0, 1, 0, 0}
				}
				if authentication {
					request := append([]byte{1, byte(len(socksUsername))}, socksUsername...)
					request = append(append(request, byte(len(socksPassword))), socksPassword...)
					if !socksWrite(t, raw, []byte{5, 2}) || !socksRead(t, raw, request) {
						return
					}
					reply = []byte{1, 1}
				}
				if !socksWrite(t, raw, reply) {
					return
				}
				var data [1]byte
				var network *net.OpError
				n, err := raw.Read(data[:])
				closed := errors.Is(err, io.EOF) || errors.As(err, &network) && !network.Timeout()
				if n != 0 || !closed {
					t.Error("failed SOCKS connection retained", err)
				}
			})
			var auth *proxy.Auth
			if authentication {
				auth = &proxy.Auth{User: socksUsername, Password: socksPassword}
			}
			dial, err := socksExampleDialer(address, auth)
			if err != nil {
				t.Fatal(err)
			}
			credentials := 0
			connector, err := NewConnector(Config{Addr: socksTarget, DialContext: dial, Credentials: func(context.Context) (Credentials, error) {
				credentials++
				return Credentials{User: testUser, Password: testPassword}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if connected, err := connector.Connect(context.Background()); connected != nil || err == nil || credentials != 0 {
				t.Fatal("SOCKS refusal reached DB authentication or direct connection", credentials, err)
			}
		})
	}
}

func startSOCKSPeers(t *testing.T, peers ...func(net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, peer := range peers {
			raw, err := listener.Accept()
			if err != nil {
				t.Error("missing expected SOCKS connection", err)
				return
			}
			func() {
				defer func() { _ = raw.Close() }()
				if err := raw.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Error(err)
					return
				}
				peer(raw)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("SOCKS peer did not stop")
		}
	})
	return listener.Addr().String()
}

func serveSOCKSConnect(t *testing.T, raw net.Conn, target string, auth bool) bool {
	t.Helper()
	want := []byte{5, 1, 0}
	method := byte(0)
	if auth {
		want, method = []byte{5, 2, 0, 2}, 2
	}
	if !socksRead(t, raw, want) || !socksWrite(t, raw, []byte{5, method}) {
		return false
	}
	if auth {
		request := append([]byte{1, byte(len(socksUsername))}, socksUsername...)
		request = append(append(request, byte(len(socksPassword))), socksPassword...)
		if !socksRead(t, raw, request) || !socksWrite(t, raw, []byte{1, 0}) {
			return false
		}
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		t.Error(err)
		return false
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Error(err)
		return false
	}
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	request = binary.BigEndian.AppendUint16(request, uint16(n))
	return socksRead(t, raw, request) && socksWrite(t, raw, []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
}

func socksRead(t *testing.T, raw net.Conn, want []byte) bool {
	t.Helper()
	data := make([]byte, len(want))
	if _, err := io.ReadFull(raw, data); err != nil || !bytes.Equal(data, want) {
		t.Errorf("SOCKS request differs: %x, want %x: %v", data, want, err)
		return false
	}
	return true
}

func socksWrite(t *testing.T, raw net.Conn, data []byte) bool {
	t.Helper()
	if n, err := raw.Write(data); err != nil || n != len(data) {
		t.Error("write SOCKS response", err)
		return false
	}
	return true
}
