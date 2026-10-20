package control

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/connect"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCancelTargetGateAndPendingBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started, ack, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var discoveries atomic.Int32
	cfg, err := config.Normalize(config.Config{User: testpeer.User, Mode: config.ModeMySQL,
		Discovery: config.DiscoveryOptions{Service: "orders", Source: func(context.Context) (config.Topology, error) {
			discoveries.Add(1)
			return config.Topology{}, errors.New("discovery must not run")
		}},
		DialContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			if network != cancelNetworkTCP || addr != cancelEntryAddress {
				t.Errorf("cancel dial changed entry: %s %s", network, addr)
			}
			client, server := net.Pipe()
			t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
			go func() {
				defer close(done)
				defer func() { _ = server.Close() }()
				p := wire.NewPacketIO(server, 64<<20)
				greeting := testpeer.ProtocolGreeting(false)
				binary.LittleEndian.PutUint32(greeting[bytes.IndexByte(greeting, 0)+1:], 43)
				if !testpeer.SendPackets(dialCtx, t, p, greeting) || !testpeer.ReadAuthResponse(dialCtx, t, p) || !testpeer.SendPackets(dialCtx, t, p, testpeer.ProtocolOK(2, nil)) || !testpeer.ServeBaseline(dialCtx, t, p, 2) {
					return
				}
				if !testpeer.ReadQuery(ctx, t, p, "KILL QUERY 42") {
					return
				}
				close(started)
				<-ack
				if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, []byte{1}) {
					return
				}
			}()
			return client, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := connect.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = configured.Close() }()
	targetCfg := cfg
	targetCfg.Addr = cancelEntryAddress
	physical := session.New(targetCfg, &session.Negotiated{Mode: config.ModeMySQL, Hello: wire.Handshake{ConnectionID: 42}, User: testpeer.User})
	defer physical.Discard()
	op, err := physical.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := op.ArmTarget()
	op.Release()
	if !physical.BeginCancellation() {
		t.Fatal("target not armed")
	}
	held, err := physical.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	canceled := make(chan error, 1)
	go func() { canceled <- CancelQuery(ctx, configured, target) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("KILL waited for target protocol gate", ctx.Err())
	}
	ended := make(chan struct{})
	go func() { physical.EndCancellation(false); close(ended) }()
	select {
	case <-ended:
		t.Fatal("execution passed pending KILL acknowledgement")
	default:
	}
	close(ack)
	if err := <-canceled; err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("pending cancellation was not retired")
	}
	if discoveries.Load() != 0 {
		t.Fatal("private cancel login consulted discovery")
	}
	if err := CancelQuery(ctx, configured, target); !errors.Is(err, dberrors.ErrResource) {
		t.Fatal("late target remained usable", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("auxiliary connection was not closed")
	}
}
