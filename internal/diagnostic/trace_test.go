package diagnostic_test

import (
	"context"
	"strings"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/diagnostic"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const traceFirst = "first"

func TestTraceIsolation(t *testing.T) {
	base := context.Background()
	first := oceanbase.WithTraceInfo(base, oceanbase.TraceInfo{Module: traceFirst, Action: "read"})
	second := oceanbase.WithTraceInfo(first, oceanbase.TraceInfo{Module: "second"})
	if wire.TraceRequestFromContext(base).App.Module != "" || wire.TraceRequestFromContext(first).App.Module != traceFirst || wire.TraceRequestFromContext(second).App.Module != "second" || wire.TraceRequestFromContext(second).App.Action != "" {
		t.Fatal("trace context leaked")
	}
	for _, info := range []oceanbase.TraceInfo{{Module: strings.Repeat("x", 65)}, {ClientInfo: "\xff"}, {Action: "x\x00y"}} {
		if err := diagnostic.ValidateTraceContext(oceanbase.WithTraceInfo(base, info)); err == nil {
			t.Fatal("invalid app info accepted")
		}
	}
}
