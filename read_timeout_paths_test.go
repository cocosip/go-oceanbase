package oceanbase

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestReadTimeoutTLS(t *testing.T) {
	for _, stage := range []string{"handshake", "query"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			clientTLS, serverTLS := testTLSConfig(t)
			configured, err := NewConnector(Config{User: testUser, ReadTimeout: 100 * time.Millisecond, TLSConfig: clientTLS, DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
				return routingTLSPeer(dialCtx, t, func(server net.Conn) {
					p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
					if !sendPackets(ctx, t, p, authGreeting(authNative, true)) || !readAuthResponse(ctx, t, p) {
						return
					}
					if stage == "handshake" {
						var data [1024]byte
						for {
							if _, e := server.Read(data[:]); e != nil {
								return
							}
						}
					}
					secure := tls.Server(server, serverTLS)
					if e := secure.HandshakeContext(ctx); e != nil {
						t.Error(e)
						return
					}
					if e := p.ReplaceConn(authPeerConn{secure}); e != nil {
						t.Error(e)
						return
					}
					if !readAuthResponse(ctx, t, p) || !sendPackets(ctx, t, p, protocolOK(2, nil)) || !serveBaseline(ctx, t, p, 2) || !readQuery(ctx, t, p, "SELECT waiting") {
						return
					}
					if _, e := p.ReadMessage(ctx); e == nil {
						t.Error("TLS timeout did not close the session")
					}
				})
			}})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(configured)
			defer func() { _ = db.Close() }()
			_, err = db.ExecContext(ctx, "SELECT waiting")
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil {
				t.Fatal("TLS path ignored independent read timeout", err)
			}
		})
	}
}

func TestReadTimeoutBatch(t *testing.T) {
	for _, strategy := range []BatchStrategy{BatchSequential, BatchMultiSend} {
		t.Run(map[BatchStrategy]string{BatchSequential: "sequential", BatchMultiSend: "pipeline"}[strategy], func(t *testing.T) {
			configured, err := NewConnector(Config{User: testUser, ReadTimeout: 100 * time.Millisecond, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, 2) {
					return
				}
				count := 1
				if strategy == BatchMultiSend {
					count = 3
				}
				for i := 1; i <= count; i++ {
					if !readCommand(ctx, t, p, batchExecutePacket(t, int64(i))) {
						return
					}
				}
				if _, e := p.ReadMessage(ctx); e == nil {
					t.Error("timed out batch continued sending")
				}
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(configured)
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := ExecBatch(ctx, db, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: strategy, ContinueOnError: true})
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil || result.Strategy != strategy || len(result.Items) != 3 || result.Items[0].Status != BatchUnknown {
				t.Fatal("batch lost read timeout or its unknown outcome", result, err)
			}
			for _, item := range result.Items[1:] {
				want := BatchNotExecuted
				if strategy == BatchMultiSend {
					want = BatchUnknown
				}
				if item.Status != want {
					t.Fatal("batch invented an outcome after read timeout", result)
				}
			}
		})
	}
}

func TestReadTimeoutPartialMessage(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bounded := &readTimeoutConn{Conn: client, timeout: 100 * time.Millisecond}
	done := make(chan error, 1)
	go func() {
		for _, b := range []byte{1, 0, 0, 0, 'x'} {
			timer := time.NewTimer(35 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				done <- ctx.Err()
				return
			}
			if _, err := server.Write([]byte{b}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	if data, err := wire.NewPacketIO(bounded, 1024).ReadMessage(ctx); err != nil || string(data) != "x" {
		t.Fatal("read timeout incorrectly bounded the whole logical message", data, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
