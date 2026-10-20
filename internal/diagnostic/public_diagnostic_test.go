package diagnostic_test

import (
	bytes "bytes"
	context "context"
	slog "log/slog"
	strings "strings"
	testing "testing"
	time "time"

	config "github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/execution"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestDiagnosticRedaction(t *testing.T) {
	var output bytes.Buffer
	var requests []config.RequestDiagnostic
	var packets []config.PacketDiagnostic
	c := diagnosticConn(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT 'credential-secret'") || !testpeer.SendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 's', 'e', 'c', 'r', 'e', 't'}) {
			return
		}
	}, func(cfg *config.Config) {
		cfg.Logger = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
		cfg.Diagnostics = config.DiagnosticOptions{SlowThreshold: time.Nanosecond, Request: func(_ context.Context, r config.RequestDiagnostic) { requests = append(requests, r) }, Packet: func(_ context.Context, p config.PacketDiagnostic) { packets = append(packets, p) }}
	})

	_, err := c.Exec(context.Background(), "SELECT 'credential-secret'", nil, execution.Options{})
	if err == nil || len(requests) != 1 || len(packets) < 2 || requests[0].SQLFingerprint == "" || requests[0].ErrorCode != 1062 {
		t.Fatal(err, requests, packets)
	}
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "SELECT") || !strings.Contains(output.String(), "duration") {
		t.Fatal(output.String())
	}
}

type diagnosticReentryHandler struct {
	c       *execution.Engine
	t       *testing.T
	entered bool
}

func (*diagnosticReentryHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *diagnosticReentryHandler) Handle(ctx context.Context, _ slog.Record) error {

	if !h.entered {
		h.entered = true
		return h.c.Ping(ctx)
	}
	return nil
}
func (h *diagnosticReentryHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *diagnosticReentryHandler) WithGroup(string) slog.Handler      { return h }
func TestDiagnosticHandlerReentry(t *testing.T) {
	handler := &diagnosticReentryHandler{t: t}
	c := diagnosticConn(t, func(ctx context.Context, p *wire.PacketIO) {
		for range 2 {
			if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
				return
			}
		}
	}, func(cfg *config.Config) { cfg.Logger = slog.New(handler) })
	handler.c = c
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil || !handler.entered {
		t.Fatal(err, handler.entered)
	}
}
func TestDiagnosticMalformedResponse(t *testing.T) {
	var records []config.RequestDiagnostic
	c := diagnosticConn(t, func(ctx context.Context, p *wire.PacketIO) {
		if testpeer.ReadCommand(ctx, t, p, []byte{14}) {
			testpeer.SendPackets(ctx, t, p, []byte{0})
		}
	}, func(cfg *config.Config) {
		cfg.Diagnostics.Request = func(_ context.Context, r config.RequestDiagnostic) { records = append(records, r) }
	})
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("malformed OK accepted")
	}
	if len(records) != 1 || !records[0].Failed {
		t.Fatal("malformed response reported success", records)
	}
}
func TestDiagnosticReentry(t *testing.T) {
	entered := false
	var c *execution.Engine
	c = diagnosticConn(t, func(ctx context.Context, p *wire.PacketIO) {
		for range 2 {
			if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
				return
			}
		}
	}, func(cfg *config.Config) {
		cfg.Diagnostics.Request = func(ctx context.Context, _ config.RequestDiagnostic) {
			if entered {
				return
			}
			entered = true
			if err := c.Ping(ctx); err != nil {
				t.Error("callback retained driver gate", err)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil || !entered {
		t.Fatal(err, entered)
	}
}
