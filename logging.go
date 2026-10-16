package oceanbase

import (
	"context"
	"log/slog"
)

func logOperation(ctx context.Context, logger *slog.Logger, level slog.Level, operation string) {
	if logger != nil && logger.Enabled(ctx, level) {
		logger.LogAttrs(ctx, level, "oceanbase", slog.String("driver", "oceanbase"), slog.String("operation", operation))
	}
}
