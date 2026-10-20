package diagnostic_test

import (
	"context"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/execution"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"testing"
	"time"
)

func diagnosticConfig() config.Config {
	return config.Config{Session: config.SessionOptions{Charset: "utf8mb4", Location: time.UTC}, Limits: config.Limits{MaxMessageBytes: 4096, MaxLOBBytes: 4096, MaxResultItems: 16, CleanupTimeout: time.Second}}
}
func diagnosticConn(t *testing.T, serve func(context.Context, *wire.PacketIO), configure ...func(*config.Config)) *execution.Engine {
	t.Helper()
	raw, err := testpeer.PeerDial(t, serve)(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := diagnosticConfig()
	for _, apply := range configure {
		apply(&cfg)
	}
	physical := session.New(cfg, &session.Negotiated{Raw: raw, Conn: raw, Packets: wire.NewPacketIO(raw, 4096), Mode: config.ModeOracle, Capabilities: wire.ClientProtocol41})
	t.Cleanup(physical.Discard)
	return execution.New(nil, physical)
}
