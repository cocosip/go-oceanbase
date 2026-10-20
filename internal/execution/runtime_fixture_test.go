package execution_test

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
)

func runtimeOperation(t *testing.T, cfg config.Config) *session.Operation {
	t.Helper()
	s := session.New(cfg, &session.Negotiated{Mode: cfg.Mode})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return op
}

func runtimeEncodeSQL(op *session.Operation, query string) ([]byte, error) {
	return command.EncodeSQL(query, op.Config().Session.Charset, op.Config().Limits.MaxMessageBytes-1)
}

func runtimeInterpolate(op *session.Operation, query string, args []driver.NamedValue) (string, error) {
	b, err := command.Interpolate(op, query, args)
	return string(b), err
}
