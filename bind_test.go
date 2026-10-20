package oceanbase

import (
	context "context"
	"database/sql"
	driver "database/sql/driver"
	errors "errors"
	"io"
	math "math"
	testing "testing"

	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

const selectBindSQL = "SELECT ?"

type failingValuer struct{ calls *int }

var errValue = errors.New("valuer rejected")

func (v failingValuer) Value() (driver.Value, error) { *v.calls++; return nil, errValue }

func TestNamedValueValidation(t *testing.T) {
	var checker any = &conn{}
	c, ok := checker.(driver.NamedValueChecker)
	if !ok {
		t.Fatal("NamedValueChecker missing")
	}
	nv := driver.NamedValue{Value: uint64(math.MaxUint64)}
	if err := c.CheckNamedValue(&nv); err != nil || nv.Value != uint64(math.MaxUint64) {
		t.Fatal(nv.Value, err)
	}
	var valueCalls int
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), selectBindSQL, failingValuer{&valueCalls}); !errors.Is(err, errValue) || valueCalls != 1 {
		t.Fatal(valueCalls, err)
	}
}

type untouchedArgumentReader struct{ reads int }

func (r *untouchedArgumentReader) Read([]byte) (int, error) {
	r.reads++
	return 0, io.EOF
}

func TestRecursiveArgumentExtensions(t *testing.T) {
	reader := new(untouchedArgumentReader)
	input := &LongData{Reader: reader, Length: -1}
	pointer := &input
	var output int64
	out := Out{Dest: &output}
	standard := sql.Out{Dest: &output}
	var cursor *Cursor
	cursorOut := CursorOut{Dest: &cursor}
	for _, value := range []any{pointer, &pointer, sql.Null[**LongData]{V: pointer, Valid: true}} {
		named := driver.NamedValue{Value: value}
		if err := (&conn{}).CheckNamedValue(&named); err != nil {
			t.Fatal(err)
		}
		source, ok := named.Value.(*streaminput.Source)
		if !ok || source.Reader != reader || source.State != &input.used || input.used.Load() || reader.reads != 0 {
			t.Fatal("recursive stream lost identity or was consumed", named.Value, reader.reads)
		}
	}
	for _, value := range []any{&out, &standard, &cursorOut, sql.Null[Out]{V: out, Valid: true}, sql.Null[*sql.Out]{V: &standard, Valid: true}} {
		named := driver.NamedValue{Value: value}
		if err := (&conn{}).CheckNamedValue(&named); err != nil {
			t.Fatal(err)
		}
		if _, ok := named.Value.(*call.Binding); !ok {
			t.Fatalf("recursive output became %T", named.Value)
		}
	}
	var nilOut *Out
	var nilPointer **LongData
	for _, value := range []any{nilOut, nilPointer, sql.Null[Out]{}, sql.Null[**LongData]{}} {
		named := driver.NamedValue{Value: value}
		if err := (&conn{}).CheckNamedValue(&named); err != nil || named.Value != nil {
			t.Fatal("nil or invalid nullable input was not NULL", named.Value, err)
		}
	}
	var nilStream *LongData
	named := driver.NamedValue{Value: nilStream}
	if err := (&conn{}).CheckNamedValue(&named); !errors.Is(err, ErrLongData) {
		t.Fatal("typed nil stream lost its validation error", err)
	}
}
