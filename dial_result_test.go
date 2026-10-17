package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestDialContextNilConnection(t *testing.T) {
	sentinel := errors.New("dial callback failed")
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, tc := range []struct {
			name     string
			err      error
			attempts int
		}{
			{"nil-error", nil, 1},
			{"error", sentinel, 1},
			{"retry", &net.OpError{Op: routeDialOperation, Net: networkTCP, Err: sentinel}, 2},
		} {
			for _, typed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/typed%t", mode, tc.name, typed), func(t *testing.T) {
					calls, credentials := 0, 0
					cfg := Config{Mode: mode, ReadTimeout: time.Second,
						Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}},
						Credentials: func(context.Context) (Credentials, error) {
							credentials++
							return Credentials{User: testUser}, nil
						},
						DialContext: func(context.Context, string, string) (net.Conn, error) {
							calls++
							if typed {
								return (*net.TCPConn)(nil), tc.err
							}
							return nil, tc.err
						},
					}
					created, err := NewConnector(cfg)
					if err != nil {
						t.Fatal(err)
					}
					db := sql.OpenDB(created)
					defer func() { _ = db.Close() }()
					defer func() {
						if value := recover(); value != nil {
							t.Errorf("nil connection panicked: %v", value)
						}
					}()
					err = db.PingContext(context.Background())
					if err == nil || tc.err != nil && !errors.Is(err, sentinel) {
						t.Fatal("dial result error lost", err)
					}
					if tc.err == nil && err.Error() != "oceanbase: dialer returned a nil connection" {
						t.Fatal("nil connection reached authentication", err)
					}
					if calls != tc.attempts || credentials != 0 || db.Stats().OpenConnections != 0 {
						t.Fatalf("calls=%d credentials=%d stats=%+v", calls, credentials, db.Stats())
					}
				})
			}
		}
	}
}

type dialResultConn struct {
	net.Conn
	closes int
}

func (c *dialResultConn) Close() error { c.closes++; return c.Conn.Close() }

func TestDialContextErrorClosesConnection(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	if err := server.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	raw := &dialResultConn{Conn: client}
	sentinel := errors.New("transport setup failed")
	created, err := NewConnector(Config{User: testUser, DialContext: func(context.Context, string, string) (net.Conn, error) {
		return raw, sentinel
	}})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(created)
	defer func() { _ = db.Close() }()
	if err = db.PingContext(context.Background()); !errors.Is(err, sentinel) || raw.closes != 1 {
		t.Fatal("failed dial did not release its connection exactly once", err, raw.closes)
	}
	if n, readErr := server.Read(make([]byte, 1)); n != 0 || !errors.Is(readErr, io.EOF) {
		t.Fatal("failed dial used or retained its transport", n, readErr)
	}
}
