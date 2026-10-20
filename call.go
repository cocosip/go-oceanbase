package oceanbase

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/session"
)

// Out supplies a database type when sql.Out's Go destination is insufficient.
// Length bounds characters (text) or bytes (binary). Precision and Scale constrain NUMBER/DECIMAL.
type Out struct {
	Dest      any
	In        bool
	Type      string
	Length    int64
	Precision int64
	Scale     int64
}

// ErrOut identifies output validation, cardinality and conversion failures.
var ErrOut = dberrors.ErrOut

// OutError identifies a logical output parameter.
type OutError = dberrors.OutError

// CursorOut returns a forward REF CURSOR from Execute. Dest must initially point to nil.
// Consume or close Execution before using the published cursor.
type CursorOut struct{ Dest **Cursor }

func makeOutput(out Out) (*call.Binding, error) {
	value := reflect.ValueOf(out.Dest)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || !value.Elem().CanSet() {
		return nil, ErrOut
	}
	var resource byte
	switch out.Dest.(type) {
	case **Cursor:
		resource = 163
	case **Blob:
		resource = 210
	case **Clob:
		resource = 211
	}
	var destination call.ResourceDestination
	if resource != 0 {
		destination = &resourceOutput{dest: out.Dest}
	}
	binding, err := call.NewBinding(call.Output{Dest: out.Dest, In: out.In, Type: out.Type, Length: out.Length, Precision: out.Precision, Scale: out.Scale}, resource, destination)
	if err != nil {
		return nil, err
	}
	if out.In {
		input, err := normalizeArgument(out.Dest)
		if err != nil {
			return nil, fmt.Errorf("%w: INOUT initial value: %w", ErrOut, err)
		}
		binding.SetInput(input)
	}
	return binding, nil
}

func hasOutputArguments(args []driver.NamedValue) bool {
	for _, arg := range args {
		switch arg.Value.(type) {
		case sql.Out, Out, CursorOut, *call.Binding:
			return true
		}
	}
	return false
}

func setOutputSession(args []driver.NamedValue, pinned Session) {
	for _, arg := range args {
		if binding, ok := arg.Value.(*call.Binding); ok {
			if destination, ok := binding.Destination().(*resourceOutput); ok {
				destination.session = pinned
			}
		}
	}
}

type resourceOutput struct {
	dest    any
	session Session
	staged  any
}

func (output *resourceOutput) Validate() error {
	value := reflect.ValueOf(output.dest)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || !value.Elem().IsNil() {
		return ErrOut
	}
	return nil
}

func (output *resourceOutput) Stage(ref session.ResourceRef, kind session.ResourceKind) error {
	if ref == (session.ResourceRef{}) {
		output.staged = nil
		return nil
	}
	if !validSession(output.session) {
		return ErrResource
	}
	handle := &resourceHandle{session: output.session, ref: ref}
	switch output.dest.(type) {
	case **Cursor:
		if kind != session.CursorResource {
			return ErrOut
		}
		output.staged = &Cursor{handle: handle}
	case **Blob:
		if kind != session.LOBResource {
			return ErrOut
		}
		output.staged = &Blob{handle: handle}
	case **Clob:
		if kind != session.LOBResource {
			return ErrOut
		}
		output.staged = &Clob{handle: handle}
	default:
		return ErrOut
	}
	return nil
}

func (output *resourceOutput) Publish() {
	switch dest := output.dest.(type) {
	case **Cursor:
		*dest = nil
		if output.staged != nil {
			*dest = output.staged.(*Cursor)
		}
	case **Blob:
		*dest = nil
		if output.staged != nil {
			*dest = output.staged.(*Blob)
		}
	case **Clob:
		*dest = nil
		if output.staged != nil {
			*dest = output.staged.(*Clob)
		}
	}
}
