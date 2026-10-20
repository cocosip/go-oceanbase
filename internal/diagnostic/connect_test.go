package diagnostic_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
)

func TestDiagnosticConnectRedaction(t *testing.T) {
	var output bytes.Buffer
	var observed oceanbase.RequestDiagnostic
	connector, err := oceanbase.NewConnector(oceanbase.Config{User: "private-user", Password: "private-password", Logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), Diagnostics: oceanbase.DiagnosticOptions{Request: func(_ context.Context, r oceanbase.RequestDiagnostic) { observed = r }}, DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("private-error") }})
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
