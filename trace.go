package oceanbase

import (
	"context"
	"fmt"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// TraceInfo is immutable request context. App fields use UTF-8 byte limits 64/64/64/256.
// IDs are opaque OceanBase wire-order 16-byte IDs; zero requests a generated trace when sampled.
type TraceInfo struct {
	ClientIdentifier, Module, Action, ClientInfo string
	TraceID, ParentSpanID                        [16]byte
}

// WithTraceInfo replaces request information; validation occurs before network activity.
// Callers can bridge their tracing library here without a driver exporter dependency.
func WithTraceInfo(ctx context.Context, info TraceInfo) context.Context {
	return wire.WithTraceRequest(ctx, wire.TraceRequest{App: wire.FullTraceInfo{ClientIdentifier: info.ClientIdentifier, Module: info.Module, Action: info.Action, ClientInfo: info.ClientInfo}, TraceID: info.TraceID, ParentSpanID: info.ParentSpanID})
}

func traceInfoFromContext(ctx context.Context) TraceInfo {
	r := wire.TraceRequestFromContext(ctx)
	return TraceInfo{ClientIdentifier: r.App.ClientIdentifier, Module: r.App.Module, Action: r.App.Action, ClientInfo: r.App.ClientInfo, TraceID: r.TraceID, ParentSpanID: r.ParentSpanID}
}

func validateTraceContext(ctx context.Context) error {
	info := traceInfoFromContext(ctx)
	if err := wire.ValidateTraceApp(wire.FullTraceInfo{ClientIdentifier: info.ClientIdentifier, Module: info.Module, Action: info.Action, ClientInfo: info.ClientInfo}); err != nil {
		return fmt.Errorf("%w: invalid trace info", ErrBind)
	}
	return nil
}
