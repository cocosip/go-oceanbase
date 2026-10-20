package statement

import (
	"context"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testNativeVersion = "4.3.0"

func sqlPlan(query string) sqltext.Plan {
	return sqltext.Plan{Query: query, SQL: query, Mode: config.ModeMySQL}
}

func testOperation(t *testing.T, cfg config.Config, mode config.Mode, version string) *session.Operation {
	t.Helper()
	if cfg.Limits.MaxMessageBytes == 0 {
		cfg.Limits.MaxMessageBytes = 1 << 20
	}
	s := session.New(cfg, &session.Negotiated{Mode: mode, OBVersion: version})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return op
}

func TestStatementParameterTypes(t *testing.T) {
	op := testOperation(t, config.Config{Execution: config.ExecutionOptions{ReuseParameterTypes: true}}, config.ModeMySQL, "")
	s := Adopt(op, 7, sqlPlan("SELECT ?"), nil)
	for i, p := range []wire.Parameter{
		{Type: 8}, {Type: 8}, {Type: 8 | 0x8000}, {Type: 6}, {Type: 6},
		{Type: 160, TypeMetadata: []byte{1}}, {Type: 160, TypeMetadata: []byte{2}}, {Type: 160, TypeMetadata: []byte{2}},
	} {
		o := s.ParameterOptions(op, nil, []wire.Parameter{p}, false)
		want := i == 1 || i == 4 || i == 7
		if o.ReuseTypes != want || s.pendingTypes[0].Data != nil {
			t.Fatal(i, o)
		}
		s.AcknowledgeTypes(nil)
		if len(p.TypeMetadata) > 0 {
			p.TypeMetadata[0] = 99
		}
	}
	s.ParameterOptions(op, nil, []wire.Parameter{{Type: 8}}, false)
	s.AcknowledgeTypes(errors.New("execute failed"))
	if s.ParameterOptions(op, nil, []wire.Parameter{{Type: 8}}, false).ReuseTypes {
		t.Fatal("failed execution acknowledged parameter types")
	}
}

func TestPrepareExecuteGate(t *testing.T) {
	for _, tc := range []struct {
		mode          config.Mode
		version       string
		enabled, want bool
	}{
		{config.ModeOracle, "2.2.76", true, true}, {config.ModeOracle, "2.2.74", true, false},
		{config.ModeOracle, "", true, false}, {config.ModeMySQL, testNativeVersion, true, false}, {config.ModeOracle, testNativeVersion, false, false},
	} {
		op := testOperation(t, config.Config{Execution: config.ExecutionOptions{PrepareExecute: tc.enabled}}, tc.mode, tc.version)
		if UsePrepareExecute(op) != tc.want {
			t.Fatal(tc)
		}
	}
	for _, o := range []config.ExecutionOptions{{StatementCacheSize: -1}, {StatementCacheSQLLimit: -1}} {
		if _, err := config.Normalize(config.Config{User: "u", Execution: o}); err == nil {
			t.Fatal("negative execution limit")
		}
	}
	cfg, err := config.ParseDSN("oceanbase://u@localhost/?prepareExecute=true&statementCacheSize=10&statementCacheSQLLimit=200&reuseParameterTypes=true&statementChecksum=true")
	if err != nil || !cfg.Execution.PrepareExecute || cfg.Execution.StatementCacheSize != 10 || cfg.Execution.StatementCacheSQLLimit != 200 || !cfg.Execution.ReuseParameterTypes || !cfg.Execution.StatementChecksum {
		t.Fatal(cfg.Execution, err)
	}
}

func TestStatementChecksumGate(t *testing.T) {
	for _, tc := range []struct {
		mode           config.Mode
		version        string
		combined, want bool
	}{
		{config.ModeOracle, "4.1.1", false, false}, {config.ModeOracle, "4.1.2", false, true},
		{config.ModeOracle, "", false, false}, {config.ModeMySQL, testNativeVersion, false, false}, {config.ModeOracle, "2.2.76", true, true},
	} {
		op := testOperation(t, config.Config{Execution: config.ExecutionOptions{StatementChecksum: true}}, tc.mode, tc.version)
		o := ExecuteOptions(op, []byte("123456789"), tc.combined)
		if o.UseChecksum != tc.want || tc.want && o.SQLChecksum != 0x58e3fa20 {
			t.Fatal(tc, o)
		}
		disabled := testOperation(t, config.Config{}, tc.mode, tc.version)
		if o = ExecuteOptions(disabled, nil, tc.combined); o != (wire.ExecuteOptions{}) {
			t.Fatal("disabled checksum", o)
		}
	}
}

func TestStatementScopeAndDeferredClose(t *testing.T) {
	op := testOperation(t, config.Config{}, config.ModeMySQL, "")
	s := Adopt(op, 0, sqlPlan("SELECT 1"), nil)
	use, err := s.AcquireLease(op)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(context.Background(), op); err != nil || s.closed {
		t.Fatal(err, s.closed)
	}
	if err = s.CheckOpen(op); err == nil {
		t.Fatal("close request accepted new use")
	}
	if err = use.Release(context.Background(), op); err != nil || !s.closed || s.leases != 0 {
		t.Fatal(err, s.closed, s.leases)
	}
	if err = use.Release(context.Background(), op); err != nil || s.leases != 0 {
		t.Fatal("lease released twice", err)
	}
	foreign := testOperation(t, config.Config{}, config.ModeMySQL, "")
	if err = s.CheckOpen(foreign); !errors.Is(err, dberrors.ErrResource) {
		t.Fatal("foreign session scope", err)
	}
	stale := Adopt(op, 99, sqlPlan("SELECT 1"), nil)
	op.AdvanceIdentity()
	if err = stale.CheckOpen(op); !errors.Is(err, dberrors.ErrResource) {
		t.Fatal(err)
	}
	if err = stale.Close(context.Background(), op); err != nil || !stale.closed {
		t.Fatal("stale close sent reused ID", err)
	}
}
