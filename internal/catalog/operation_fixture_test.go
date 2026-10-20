package catalog_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func catalogOperation(t *testing.T, cfg config.Config, serve func(context.Context, *wire.PacketIO)) *session.Operation {
	t.Helper()
	if cfg.Mode == 0 {
		cfg.Mode = config.ModeMySQL
	}
	if cfg.Session.Charset == "" {
		cfg.Session.Charset = "utf8mb4"
	}
	if cfg.Limits.MaxResultItems == 0 {
		cfg.Limits.MaxResultItems = 64
	}
	if cfg.Limits.MaxLOBBytes == 0 {
		cfg.Limits.MaxLOBBytes = 1 << 20
	}
	if cfg.Limits.MaxMessageBytes == 0 {
		cfg.Limits.MaxMessageBytes = 1 << 20
	}
	if cfg.Limits.CleanupTimeout == 0 {
		cfg.Limits.CleanupTimeout = time.Second
	}
	p := &session.Negotiated{Mode: cfg.Mode, Capabilities: wire.ClientProtocol41, OK: wire.OK{Status: 2}, OBVersion: "4.3.5.1"}
	if cfg.Mode == config.ModeOracle {
		p.OK.Status = 6
	}
	if serve != nil {
		client, server := net.Pipe()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		deadline, _ := ctx.Deadline()
		_ = server.SetDeadline(deadline)
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = server.Close() }()
			serve(ctx, wire.NewPacketIO(server, cfg.Limits.MaxMessageBytes))
		}()
		p.Raw, p.Conn, p.Packets = client, client, wire.NewPacketIO(client, cfg.Limits.MaxMessageBytes)
		t.Cleanup(func() { cancel(); _ = client.Close(); _ = server.Close(); <-done })
	}
	s := session.New(cfg, p)
	op, err := s.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release(); s.Discard() })
	return op
}
