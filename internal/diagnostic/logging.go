package diagnostic

import (
	"context"
	"log/slog"

	"github.com/cocosip/go-oceanbase/config"
)

// Emit delivers a redacted request to the configured logger, then its request callback.
func Emit(ctx context.Context, cfg config.Config, d config.RequestDiagnostic) {
	level := slog.LevelDebug
	if cfg.Diagnostics.SlowThreshold > 0 && d.Duration >= cfg.Diagnostics.SlowThreshold {
		level = slog.LevelWarn
	}
	if logger := cfg.Logger; logger != nil && logger.Enabled(ctx, level) {
		logger.LogAttrs(ctx, level, "oceanbase", slog.String("driver", "oceanbase"), slog.String("operation", d.Operation), slog.Int("mode", int(d.Mode)), slog.Uint64("connection_id", uint64(d.ConnectionID)), slog.Uint64("request_id", uint64(d.RequestID)), slog.Duration("duration", d.Duration), slog.Int64("read_bytes", d.ReadBytes), slog.Int64("write_bytes", d.WriteBytes), slog.Uint64("error_code", uint64(d.ErrorCode)), slog.String("sql_state", d.SQLState), slog.Bool("failed", d.Failed))
	}
	if callback := cfg.Diagnostics.Request; callback != nil {
		callback(ctx, d)
	}
}
