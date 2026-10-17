package oceanbase

import (
	"context"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// DiagnosticOptions bounds synchronous diagnostic delivery after releasing the driver gate.
// SQL and packets are represented by fingerprints and metadata, never by raw payloads.
type DiagnosticOptions struct {
	SlowThreshold           time.Duration
	MaxRequests, MaxPackets int
	Request                 func(context.Context, RequestDiagnostic)
	Packet                  func(context.Context, PacketDiagnostic)
}

// RequestDiagnostic describes one physical protocol command, including consumer time for Rows.
type RequestDiagnostic struct {
	Operation                        string
	Mode                             Mode
	ConnectionID, RequestID          uint32
	Duration                         time.Duration
	ReadBytes, WriteBytes            int64
	SQLFingerprint                   string
	SQLBytes                         int
	ErrorCode                        uint16
	SQLState                         string
	Failed                           bool
	DroppedPackets, DroppedRequests  int
	TraceID, SpanID                  [16]byte
	QueryStartMicros, QueryEndMicros int64
}

// PacketDiagnostic describes a logical message without exposing its payload.
type PacketDiagnostic struct {
	RequestID uint32
	Command   byte
	Write     bool
	Bytes     int64
	Sequence  byte
	Failed    bool
}

func (o DiagnosticOptions) validate() error {
	if o.SlowThreshold < 0 || o.MaxRequests < 0 || o.MaxRequests > 1024 || o.MaxPackets < 0 || o.MaxPackets > 1024 {
		return &ConfigError{Field: "Diagnostics"}
	}
	return nil
}

func (c *conn) enableDiagnostics() {
	if c.session != nil && c.session.packets != nil && (c.cfg.Logger != nil || c.cfg.Diagnostics.Request != nil || c.cfg.Diagnostics.Packet != nil) {
		c.session.packets.EnableDiagnostics(c.cfg.Diagnostics.MaxRequests, c.cfg.Diagnostics.MaxPackets)
	}
}

func (c *conn) diagnosticSnapshots() ([]wire.RequestDiagnostic, int) {
	if c.session == nil || c.session.packets == nil {
		return nil, 0
	}
	if c.closed.Load() {
		c.session.packets.MarkDiagnosticFailure()
	}
	return c.session.packets.TakeDiagnostics()
}

func (c *conn) emitDiagnostics(records []wire.RequestDiagnostic, dropped int) {
	for _, r := range records {
		d := RequestDiagnostic{Operation: diagnosticOperation(r.Command), Mode: c.session.mode, ConnectionID: c.session.hello.ConnectionID, RequestID: r.RequestID, Duration: r.Duration, ReadBytes: r.ReadBytes, WriteBytes: r.WriteBytes, SQLFingerprint: r.SQLFingerprint, SQLBytes: r.SQLBytes, ErrorCode: r.ErrorCode, SQLState: r.SQLState, Failed: r.Failed, DroppedPackets: r.DroppedPackets, DroppedRequests: dropped}
		d.TraceID, d.SpanID = r.TraceID, r.SpanID
		d.QueryStartMicros, d.QueryEndMicros = r.QueryStartMicros, r.QueryEndMicros
		emitDiagnostic(r.Context, c.cfg, d)
		if callback := c.cfg.Diagnostics.Packet; callback != nil {
			for _, p := range r.Packets {
				callback(r.Context, PacketDiagnostic{RequestID: d.RequestID, Command: r.Command, Write: p.Write, Bytes: p.Bytes, Sequence: p.Sequence, Failed: p.Failed})
			}
		}
	}
}

func diagnosticOperation(command byte) string {
	switch command {
	case 1:
		return "close"
	case 3:
		return "query"
	case 14:
		return "ping"
	case 17:
		return "change-user"
	case 22:
		return "prepare"
	case 23:
		return "execute"
	case 24, 162:
		return "long-data"
	case 25:
		return "statement-close"
	case 9, 28:
		return "fetch"
	case 161:
		return "prepare-execute"
	default:
		return "command"
	}
}
