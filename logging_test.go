package oceanbase

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
)

func TestLoggerIsolation(t *testing.T) {
	var first, second, fallback bytes.Buffer
	options := &slog.HandlerOptions{Level: slog.LevelDebug}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&fallback, options)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range []struct {
		user   string
		logger *slog.Logger
	}{
		{"first-secret", slog.New(slog.NewJSONHandler(&first, options))},
		{"second-secret", slog.New(slog.NewJSONHandler(&second, options))},
		{"silent-secret", nil},
	} {
		c, err := NewConnector(Config{User: tc.user, Password: "never-log-password", Logger: tc.logger, DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("test dial failure") }})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Connect(context.Background())
	}
	if first.Len() == 0 || second.Len() == 0 || fallback.Len() != 0 {
		t.Fatal("logger not injected or nil fell back to default")
	}
	if strings.Count(first.String(), "\n") != 1 || strings.Count(second.String(), "\n") != 1 {
		t.Fatal("connector log output crossed instances")
	}
	for _, output := range []string{first.String(), second.String(), fallback.String()} {
		if strings.Contains(output, "secret") || strings.Contains(output, "password") || strings.Contains(output, "oceanbase://") {
			t.Fatal("credentials leaked to log")
		}
	}
}
