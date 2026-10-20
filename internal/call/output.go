// Package call owns output binding, conversion and publication.
package call

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

// Output contains the existing output contract without a dependency on the root facade.
type Output struct {
	Dest                     any
	In                       bool
	Type                     string
	Length, Precision, Scale int64
}

// ResourceDestination stages a public handle without assigning or publishing it.
type ResourceDestination interface {
	Validate() error
	Stage(session.ResourceRef, session.ResourceKind) error
	Publish()
}

// Binding retains one normalized scalar or resource output slot.
type Binding struct {
	Output
	input       any
	custom      bool
	parameter   int
	resource    byte
	destination ResourceDestination
	ref         session.ResourceRef
}

// NewBinding validates an output destination before any protocol activity.
func NewBinding(out Output, resource byte, destination ResourceDestination) (*Binding, error) {
	if resource == 0 {
		return newScalarBinding(out)
	}
	name := strings.ToUpper(strings.TrimSpace(out.Type))
	expected := name == valueformat.TypeNameRefCursor && resource == 163 || name == valueformat.TypeNameBlob && resource == 210 || (name == valueformat.TypeNameClob || name == valueformat.TypeNameNClob) && resource == 211
	if !expected || destination == nil || out.In || out.Length != 0 || out.Precision != 0 || out.Scale != 0 {
		return nil, dberrors.ErrOut
	}
	if err := destination.Validate(); err != nil {
		return nil, err
	}
	out.Type = name
	return &Binding{Output: out, resource: resource, destination: destination}, nil
}

// SetInput stores the normalized initial INOUT value.
func (b *Binding) SetInput(value any) { b.input = value }

// Input returns the normalized initial INOUT value.
func (b *Binding) Input() any { return b.input }

// ResourceType identifies a cursor or native LOB output.
func (b *Binding) ResourceType() byte { return b.resource }

// Destination returns the public boundary slot without assigning or publishing it.
func (b *Binding) Destination() ResourceDestination { return b.destination }

// ValidateMode checks the declared type against the compatibility mode.
func (b *Binding) ValidateMode(mode config.Mode) error { return b.validateMode(mode) }

type assignment struct {
	op             *session.Operation
	transactionCtx context.Context
}

func (c *assignment) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.transactionCtx != nil {
		return c.transactionCtx.Err()
	}
	return nil
}

// Assign preserves scalar Scanner order and publishes resource destinations only after all checks.
// Both contexts remain live while Scanner callbacks may cancel either one.
//
//revive:disable-next-line:context-as-argument
func Assign(ctx context.Context, op *session.Operation, transactionCtx context.Context, bindings []*Binding, values []driver.Value) error {
	if len(bindings) != len(values) {
		return dberrors.ErrOut
	}
	c := &assignment{op: op, transactionCtx: transactionCtx}
	staged := make([]reflect.Value, len(bindings))
	for i, b := range bindings {
		if b.resource != 0 {
			if err := b.destination.Validate(); err != nil {
				return err
			}
			if b.ref != (session.ResourceRef{}) {
				if err := op.Validate(b.ref); err != nil {
					return err
				}
			}
			continue
		}
		if err := c.validateOutputValue(b, values[i]); err != nil {
			return &dberrors.OutError{Parameter: b.parameterNumber(i + 1), Err: err}
		}
		if b.custom {
			continue
		}
		target := reflect.ValueOf(b.Dest).Elem()
		temp := reflect.New(target.Type())
		temp.Elem().Set(target)
		if err := c.scanOutput(temp.Interface(), values[i]); err != nil {
			return &dberrors.OutError{Parameter: b.parameterNumber(i + 1), Err: err}
		}
		staged[i] = temp.Elem()
	}
	for i, b := range bindings {
		if err := c.check(ctx); err != nil {
			return err
		}
		if b.resource != 0 {
			continue
		}
		if b.custom {
			if err := b.Dest.(sql.Scanner).Scan(values[i]); err != nil {
				return &dberrors.OutError{Parameter: b.parameterNumber(i + 1), Partial: true, Err: err}
			}
		} else {
			reflect.ValueOf(b.Dest).Elem().Set(staged[i])
		}
	}
	if err := c.check(ctx); err != nil {
		return err
	}
	for _, b := range bindings {
		if b.resource == 0 {
			continue
		}
		if b.ref != (session.ResourceRef{}) {
			if err := op.Validate(b.ref); err != nil {
				return err
			}
		}
	}
	for _, b := range bindings {
		if b.resource == 0 {
			continue
		}
		if b.ref != (session.ResourceRef{}) {
			if err := op.Publish(b.ref); err != nil {
				return err
			}
		}
		b.destination.Publish()
	}
	return nil
}

// ValidateValue checks an output value against its length and numeric constraints.
func ValidateValue(op *session.Operation, b *Binding, value driver.Value) error {
	return (&assignment{op: op}).validateOutputValue(b, value)
}
