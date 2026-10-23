package parameters

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

type argumentSource struct {
	value any
	calls *int
	err   error
}

func (s argumentSource) Resolve() (any, error) { *s.calls++; return s.value, s.err }

type unreadReader struct{ reads *int }

func (r unreadReader) Read([]byte) (int, error) { *r.reads++; return 0, io.EOF }

func parameterOperation(t *testing.T, mode config.Mode) *session.Operation {
	t.Helper()
	cfg, err := config.Normalize(config.Config{User: "app", Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(cfg, &session.Negotiated{Mode: mode, OBVersion: "4.3.5.0"})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return op
}

func TestBindConversionOnce(t *testing.T) {
	plan, err := sqltext.BuildBindPlan("SELECT :a,:A FROM dual", config.ModeOracle)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	value, err := normalize(countingValuer{&calls})
	if err != nil {
		t.Fatal(err)
	}
	values, err := Bind(plan, []driver.NamedValue{{Name: "a", Value: value}})
	if err != nil || calls != 1 || len(values) != 2 || values[0] != int64(42) || values[1] != int64(42) {
		t.Fatal(values, calls, err)
	}
	var nilValuer *countingValuer
	if v, err := normalize(nilValuer); err != nil || v != nil {
		t.Fatal(v, err)
	}
}

func TestArgumentResolutionOrder(t *testing.T) {
	var first, second int
	want := errors.New("current argument failed")
	if _, err := ResolveArguments([]driver.NamedValue{{Value: argumentSource{calls: &first, err: want}}, {Value: argumentSource{calls: &second}}}); !errors.Is(err, want) || first != 1 || second != 0 {
		t.Fatal(first, second, err)
	}
	var reads int
	state := new(atomic.Bool)
	source := &streaminput.Source{Reader: unreadReader{&reads}, Length: -1, State: state}
	args, err := ResolveArguments([]driver.NamedValue{{Name: "a", Value: argumentSource{value: source, calls: &first}}})
	if err != nil {
		t.Fatal(err)
	}
	op := parameterOperation(t, config.ModeOracle)
	if err = Prevalidate(op, "SELECT :a,:A FROM dual", args); err != nil {
		t.Fatal(err)
	}
	plan, err := sqltext.BuildBindPlan("SELECT :a,:A FROM dual", config.ModeOracle)
	if err != nil {
		t.Fatal(err)
	}
	values, err := Bind(plan, args)
	if err != nil {
		t.Fatal(err)
	}
	params, err := Encode(context.Background(), op, nil, values, 1024)
	if err != nil || len(params) != 2 || !params[0].LongData || !params[1].LongData || reads != 0 || state.Load() || values[0] != source || values[1] != source {
		t.Fatal(params, values, reads, state.Load(), err)
	}
}

func TestBindDeferredResolutionOrder(t *testing.T) {
	plan, err := sqltext.BuildBindPlan("SELECT :a,:A,:b FROM dual", config.ModeOracle)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		names     []string
		wantCalls [2]int
		wantError bool
	}{
		{"fanout", []string{"a", "b"}, [2]int{1, 1}, false},
		{"duplicate", []string{"a", "A"}, [2]int{1, 0}, true},
		{"missing name", []string{"a", ""}, [2]int{1, 0}, true},
		{"missing slot", []string{"a", "c"}, [2]int{1, 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [2]int
			args := []driver.NamedValue{{Name: tc.names[0], Value: argumentSource{value: int64(7), calls: &calls[0]}}, {Name: tc.names[1], Value: argumentSource{value: int64(9), calls: &calls[1]}}}
			values, bindErr := Bind(plan, args)
			if (bindErr != nil) != tc.wantError || calls != tc.wantCalls {
				t.Fatal(values, calls, bindErr)
			}
			if !tc.wantError && (values[0] != int64(7) || values[1] != int64(7) || values[2] != int64(9)) {
				t.Fatal(values)
			}
		})
	}
}

func TestPrepareArgumentsStopsAtCurrentLongData(t *testing.T) {
	op := parameterOperation(t, config.ModeOracle)
	var calls [2]int
	var reader *strings.Reader
	args := []driver.NamedValue{{Name: "a", Value: argumentSource{value: &streaminput.Source{Reader: reader, State: new(atomic.Bool)}, calls: &calls[0]}}, {Name: "b", Value: argumentSource{value: int64(2), calls: &calls[1]}}}
	if _, err := PrepareArguments(op, "SELECT :a,:b FROM dual", args); !errors.Is(err, dberrors.ErrLongData) || calls != [2]int{1, 0} {
		t.Fatal(calls, err)
	}
	mySQL := parameterOperation(t, config.ModeMySQL)
	if _, err := PrepareArguments(mySQL, "SELECT ?", args[:1]); !errors.Is(err, dberrors.ErrBind) || calls != [2]int{1, 0} {
		t.Fatal(calls, err)
	}
}

func TestPrevalidationAndEncodedBudget(t *testing.T) {
	op := parameterOperation(t, config.ModeOracle)
	if err := Prevalidate(op, "SELECT :x FROM dual", nil); !errors.Is(err, dberrors.ErrBind) {
		t.Fatal(err)
	}
	if err := Prevalidate(op, strings.Repeat("x", int(op.Config().Limits.MaxMessageBytes)), nil); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
	var reader *strings.Reader
	if err := Prevalidate(op, "SELECT :x FROM dual", []driver.NamedValue{{Name: "x", Value: &streaminput.Source{Reader: reader, State: new(atomic.Bool)}}}); !errors.Is(err, dberrors.ErrLongData) {
		t.Fatal(err)
	}
	if _, err := Encode(context.Background(), op, nil, []any{"ab", "cd"}, 5); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
	if values, err := Encode(context.Background(), op, nil, []any{"ab", "cd"}, 6); err != nil || len(values) != 2 {
		t.Fatal(values, err)
	}
}

func TestOracleBytesLOBAndMessageBudgets(t *testing.T) {
	s := session.New(config.Config{Limits: config.Limits{MaxLOBBytes: 4}}, &session.Negotiated{Mode: config.ModeOracle})
	op, err := s.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	for _, tc := range []struct {
		value    any
		budget   int64
		want     string
		tooLarge bool
	}{
		{[]byte{0, 1, 127, 255}, 9, "00017fff", false},
		{[]byte{0, 1, 127, 255}, 8, "", true},
		{[]byte{0, 1, 127, 255, 0}, 20, "", true},
		{types.Raw{0, 1, 127, 255}, 5, string([]byte{0, 1, 127, 255}), false},
		{types.Raw{0, 1, 127, 255, 0}, 20, "", true},
	} {
		p, err := Encode(t.Context(), op, nil, []any{tc.value}, tc.budget)
		if tc.tooLarge {
			if !errors.Is(err, wire.ErrMessageTooLarge) {
				t.Fatalf("budget=%d error=%v", tc.budget, err)
			}
		} else if err != nil || len(p) != 1 || string(p[0].Data) != tc.want {
			t.Fatalf("budget=%d params=%v error=%v", tc.budget, p, err)
		}
	}
}
