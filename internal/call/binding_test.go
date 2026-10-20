package call

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

func assignmentOperation(t *testing.T) *session.Operation {
	t.Helper()
	s := session.New(config.Config{Session: config.SessionOptions{Location: time.UTC}, Limits: config.Limits{MaxMessageBytes: 4096, MaxLOBBytes: 4096}}, &session.Negotiated{})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return op
}

type failingOutScanner struct{ calls int }

func (s *failingOutScanner) Scan(any) error { s.calls++; return errors.New("scanner rejected output") }
func TestOutFinalization(t *testing.T) {
	op := assignmentOperation(t)
	first := int64(7)
	second := int8(8)
	a, err := newScalarBinding(Output{Dest: &first})
	if err != nil {
		t.Fatal(err)
	}
	b, err := newScalarBinding(Output{Dest: &second})
	if err != nil {
		t.Fatal(err)
	}
	if err = Assign(context.Background(), op, nil, []*Binding{a, b}, []driver.Value{int64(1), int64(128)}); err == nil || first != 7 || second != 8 {
		t.Fatalf("overflow partly updated outputs: %d %d %v", first, second, err)
	}
	if err = Assign(context.Background(), op, nil, []*Binding{a}, []driver.Value{nil}); err == nil || first != 7 {
		t.Fatal("NULL became a zero value", first, err)
	}
	var nullable sql.Null[int64]
	n, err := newScalarBinding(Output{Dest: &nullable})
	if err != nil {
		t.Fatal(err)
	}
	if err = Assign(context.Background(), op, nil, []*Binding{a, n}, []driver.Value{int64(9), nil}); err != nil || first != 9 || nullable.Valid {
		t.Fatal(first, nullable, err)
	}
	scanner := &failingOutScanner{}
	s, err := newScalarBinding(Output{Dest: scanner, Type: valueformat.TypeNameVarchar})
	if err != nil {
		t.Fatal(err)
	}
	err = Assign(context.Background(), op, nil, []*Binding{a, s}, []driver.Value{int64(10), "rejected"})
	var failure *dberrors.OutError
	if !errors.As(err, &failure) || !failure.Partial || failure.Parameter != 2 || first != 10 || scanner.calls != 1 {
		t.Fatal(first, scanner.calls, err)
	}
}

func TestOutDeclaredLength(t *testing.T) {
	op := assignmentOperation(t)
	var binaryValue []byte
	binaryOut, err := newScalarBinding(Output{Dest: &binaryValue, Type: valueformat.TypeNameVarbinary, Length: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err = Assign(context.Background(), op, nil, []*Binding{binaryOut}, []driver.Value{"中"}); err == nil || binaryValue != nil {
		t.Fatal("binary bound counted characters", binaryValue, err)
	}
	var textValue string
	textOut, err := newScalarBinding(Output{Dest: &textValue, Type: valueformat.TypeNameVarchar, Length: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = Assign(context.Background(), op, nil, []*Binding{textOut}, []driver.Value{[]byte("中")}); err != nil || textValue != "中" {
		t.Fatal("text bound counted bytes", textValue, err)
	}
}

func TestOutPrecisionAndNullableTime(t *testing.T) {
	op := assignmentOperation(t)
	for _, tc := range []struct {
		precision, scale int64
		value            string
		valid            bool
	}{{2, 4, "0.0012", true}, {2, 4, "0.12", false}, {2, 4, "0", true}, {2, -2, "1200", true}, {2, -2, "1230", false}, {2, -2, "12300", false}, {3, 2, "9.99", true}, {3, 2, "10", false}} {
		b := &Binding{Output: Output{Type: valueformat.TypeNameNumber, Precision: tc.precision, Scale: tc.scale}}
		if err := ValidateValue(op, b, tc.value); (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
	var timestamp sql.Null[time.Time]
	b, err := newScalarBinding(Output{Dest: &timestamp})
	if err != nil {
		t.Fatal(err)
	}
	if err = Assign(context.Background(), op, nil, []*Binding{b}, []driver.Value{"2026-01-02 03:04:05"}); err != nil || !timestamp.Valid || timestamp.V.Year() != 2026 {
		t.Fatal(timestamp, err)
	}
}
