// Package diagnostic validates trace context and delivers redacted driver diagnostics.
package diagnostic

import (
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// EmitRecords maps protocol snapshots and preserves logger, request, then packet delivery order.
// Callers release their operation gate before delivering records.
func EmitRecords(cfg config.Config, mode config.Mode, connectionID uint32, records []wire.RequestDiagnostic, dropped int) {
	for _, r := range records {
		d := config.RequestDiagnostic{Operation: diagnosticOperation(r.Command), Mode: mode, ConnectionID: connectionID, RequestID: r.RequestID, Duration: r.Duration, ReadBytes: r.ReadBytes, WriteBytes: r.WriteBytes, SQLFingerprint: r.SQLFingerprint, SQLBytes: r.SQLBytes, ErrorCode: r.ErrorCode, SQLState: r.SQLState, Failed: r.Failed, DroppedPackets: r.DroppedPackets, DroppedRequests: dropped}
		d.TraceID, d.SpanID = r.TraceID, r.SpanID
		d.QueryStartMicros, d.QueryEndMicros = r.QueryStartMicros, r.QueryEndMicros
		Emit(r.Context, cfg, d)
		if callback := cfg.Diagnostics.Packet; callback != nil {
			for _, p := range r.Packets {
				callback(r.Context, config.PacketDiagnostic{RequestID: d.RequestID, Command: r.Command, Write: p.Write, Bytes: p.Bytes, Sequence: p.Sequence, Failed: p.Failed})
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
