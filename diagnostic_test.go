package oceanbase

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestDiagnosticRedaction(t *testing.T) {
	var output bytes.Buffer
	var requests []RequestDiagnostic
	var packets []PacketDiagnostic
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT 'credential-secret'") || !sendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 's', 'e', 'c', 'r', 'e', 't'}) {
			return
		}
	})
	c.cfg.Logger = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c.cfg.Diagnostics = DiagnosticOptions{SlowThreshold: time.Nanosecond, Request: func(_ context.Context, r RequestDiagnostic) { requests = append(requests, r) }, Packet: func(_ context.Context, p PacketDiagnostic) { packets = append(packets, p) }}
	_, err := c.ExecContext(context.Background(), "SELECT 'credential-secret'", nil)
	if err == nil || len(requests) != 1 || len(packets) < 2 || requests[0].SQLFingerprint == "" || requests[0].ErrorCode != 1062 {
		t.Fatal(err, requests, packets)
	}
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "SELECT") || !strings.Contains(output.String(), "duration") {
		t.Fatal(output.String())
	}
}

type diagnosticReentryHandler struct {
	c       *conn
	t       *testing.T
	entered bool
}

func (*diagnosticReentryHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *diagnosticReentryHandler) Handle(ctx context.Context, _ slog.Record) error {
	if len(h.c.mu) != 0 {
		h.t.Error("slog called under driver gate")
	}
	if !h.entered {
		h.entered = true
		return h.c.Ping(ctx)
	}
	return nil
}
func (h *diagnosticReentryHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *diagnosticReentryHandler) WithGroup(string) slog.Handler      { return h }

func TestDiagnosticHandlerReentry(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		for range 2 {
			if !serveOK(ctx, t, p, []byte{14}, 6) {
				return
			}
		}
	})
	handler := &diagnosticReentryHandler{c: c, t: t}
	c.cfg.Logger = slog.New(handler)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil || !handler.entered {
		t.Fatal(err, handler.entered)
	}
}

func TestDiagnosticConnectRedaction(t *testing.T) {
	var output bytes.Buffer
	var observed RequestDiagnostic
	connector, err := NewConnector(Config{User: "private-user", Password: "private-password", Logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), Diagnostics: DiagnosticOptions{Request: func(_ context.Context, r RequestDiagnostic) { observed = r }}, DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("private-error") }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connector.Connect(context.Background()); err == nil {
		t.Fatal("dial unexpectedly succeeded")
	}
	if observed.Operation != "connect" || !observed.Failed || observed.Duration < 0 || strings.Contains(output.String(), "private") {
		t.Fatal(observed, output.String())
	}
}

func TestDiagnosticMalformedResponse(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if readCommand(ctx, t, p, []byte{14}) {
			sendPackets(ctx, t, p, []byte{0})
		}
	})
	var records []RequestDiagnostic
	c.cfg.Diagnostics.Request = func(_ context.Context, r RequestDiagnostic) { records = append(records, r) }
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("malformed OK accepted")
	}
	if len(records) != 1 || !records[0].Failed {
		t.Fatal("malformed response reported success", records)
	}
}

func TestDiagnosticReentry(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		for range 2 {
			if !serveOK(ctx, t, p, []byte{14}, 6) {
				return
			}
		}
	})
	entered := false
	c.cfg.Diagnostics.Request = func(ctx context.Context, _ RequestDiagnostic) {
		if entered {
			return
		}
		entered = true
		if err := c.Ping(ctx); err != nil {
			t.Error("callback retained driver gate", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil || !entered {
		t.Fatal(err, entered)
	}
}
