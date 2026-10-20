package call

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/materialize"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// State owns one execution's output bindings, hidden rows and pending resources.
type State struct {
	epoch          session.Epoch
	resourceEpoch  bool
	parent         *statement.Statement
	bindings       []*Binding
	physical       []int
	values         []driver.Value
	columns        []wire.Column
	complexColumns []wire.Column
	complexRows    [][]byte
	complexBytes   int64
	vars           []string
	longArgs       []driver.NamedValue
	seen           bool
	err            error
}

// NewState associates physical output slots with logical parameters.
func NewState(op *session.Operation, plan sqltext.Plan, values []any) (*State, error) {
	s := &State{epoch: op.Epoch()}
	indices := make(map[*Binding]int)
	destinations := make(map[any]bool)
	for i, value := range values {
		b, ok := value.(*Binding)
		if !ok {
			continue
		}
		index, exists := indices[b]
		if !exists {
			if destinations[b.Dest] {
				return nil, fmt.Errorf("%w: distinct output arguments alias the same destination", dberrors.ErrOut)
			}
			if err := b.validateMode(plan.Mode); err != nil {
				return nil, err
			}
			b.parameter = plan.Slots[i].Index + 1
			index = len(s.bindings)
			indices[b] = index
			destinations[b.Dest] = true
			s.bindings = append(s.bindings, b)
			s.resourceEpoch = s.resourceEpoch || b.resource != 0
		}
		s.physical = append(s.physical, index)
	}
	return s, nil
}

// CheckEpoch rejects stale resource outputs after a session epoch change.
func (s *State) CheckEpoch(op *session.Operation) error {
	if s != nil && s.resourceEpoch && s.epoch != op.Epoch() {
		return dberrors.ErrResource
	}
	return nil
}

// HasResources reports whether output handles require epoch validation.
func (s *State) HasResources() bool { return s != nil && s.resourceEpoch }

// HasDeferredComplex reports buffered complex output bytes that still require descriptors.
func (s *State) HasDeferredComplex() bool { return s != nil && len(s.complexRows) != 0 }

// Seen reports whether a hidden output row has been captured.
func (s *State) Seen() bool { return s != nil && s.seen }

// ParentStatement identifies the concrete statement retained by child resources.
func (s *State) ParentStatement() *statement.Statement {
	if s == nil {
		return nil
	}
	return s.parent
}

// Parent retains the concrete statement that may own child outputs.
func (s *State) Parent(p *statement.Statement) { s.parent = p }

// LongArguments returns streaming inputs awaiting the MySQL call.
func (s *State) LongArguments() []driver.NamedValue { return s.longArgs }

// Bindings returns unique logical output bindings in assignment order.
func (s *State) Bindings() []*Binding { return s.bindings }

// Values returns the staged values awaiting assignment.
func (s *State) Values() []driver.Value { return s.values }

// Capture validates hidden row cardinality and repeated physical bindings.
func (s *State) Capture(values []driver.Value, physical bool) {
	if s.seen || len(values) != len(s.bindings) && !physical || physical && len(values) != len(s.physical) {
		s.err = fmt.Errorf("%w: output row cardinality or column count", dberrors.ErrOut)
		return
	}
	s.seen = true
	s.values = make([]driver.Value, len(s.bindings))
	assigned := make([]bool, len(s.bindings))
	for i, value := range values {
		index := i
		if physical {
			index = s.physical[i]
		}
		if assigned[index] && !reflect.DeepEqual(s.values[index], value) {
			s.err = fmt.Errorf("%w: repeated output binding returned conflicting values", dberrors.ErrOut)
			return
		}
		if data, ok := value.([]byte); ok {
			value = slices.Clone(data)
		}
		s.values[index] = value
		assigned[index] = true
	}
}

// ReadHidden consumes output rows without publishing destinations.
func (s *State) ReadHidden(ctx context.Context, op *session.Operation, r *command.RawResponse) error {
	hasComplex := false
	for _, column := range r.Columns() {
		hasComplex = hasComplex || column.Type == 160
	}
	if hasComplex {
		if s.complexColumns != nil {
			s.err = dberrors.ErrOut
		}
		s.complexColumns = r.Columns()
	}
	for !r.Done() {
		if hasComplex {
			data, err := r.NextPacket(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			s.complexBytes += int64(len(data)) + 24
			if s.complexBytes > op.Config().Limits.MaxMessageBytes {
				op.Discard()
				return wire.ErrMessageTooLarge
			}
			s.complexRows = append(s.complexRows, data)
			continue
		}
		fields, err := r.NextFields(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		values, err := materialize.Row(op, r.Columns(), true, fields, true)
		if err != nil {
			return err
		}
		s.Capture(values, true)
		if s.seen && s.err == nil {
			s.columns = make([]wire.Column, len(s.bindings))
			for i, column := range r.Columns() {
				s.columns[s.physical[i]] = column
			}
		}
	}
	return nil
}

// DecodeComplex resolves buffered complex output fields after response completion.
func (s *State) DecodeComplex(ctx context.Context, op *session.Operation, resolver *catalog.Resolver) error {
	if len(s.complexRows) == 0 {
		return nil
	}
	if err := errors.Join(resolver.DescribeColumns(ctx, op, s.complexColumns), s.CheckEpoch(op)); err != nil {
		return err
	}
	d := materialize.New(op, resolver)
	for _, raw := range s.complexRows {
		fields, err := wire.DecodeBinaryRow(raw, s.complexColumns)
		if err != nil {
			return err
		}
		values, err := materialize.Row(op, s.complexColumns, true, fields, true)
		if err != nil {
			return err
		}
		if err = errors.Join(d.ComplexValues(ctx, s.complexColumns, values), s.CheckEpoch(op)); err != nil {
			return err
		}
		s.Capture(values, true)
		if s.err != nil {
			return s.err
		}
	}
	s.columns = make([]wire.Column, len(s.bindings))
	for i, column := range s.complexColumns {
		s.columns[s.physical[i]] = column
	}
	s.complexRows = nil
	return nil
}

// ResourceValue is one unregistered output value; execution dispatches it to the concrete owner.
type ResourceValue struct {
	Index  int
	Type   byte
	Column wire.Column
	Value  driver.Value
}

// Resources returns validated native resources awaiting owner registration.
func (s *State) Resources() ([]ResourceValue, error) {
	if !s.HasResources() {
		return nil, nil
	}
	if len(s.columns) != len(s.values) {
		return nil, dberrors.ErrOut
	}
	seen := make(map[materialize.RefCursorID]bool)
	var resources []ResourceValue
	for i, b := range s.bindings {
		if b.resource == 0 {
			continue
		}
		if s.columns[i].Type != b.resource {
			return nil, dberrors.ErrOut
		}
		if s.values[i] != nil && b.resource == 163 {
			id, ok := s.values[i].(materialize.RefCursorID)
			if !ok || s.parent == nil || uint32(id) == s.parent.ID() || seen[id] {
				return nil, dberrors.ErrOut
			}
			seen[id] = true
		}
		resources = append(resources, ResourceValue{Index: i, Type: b.resource, Column: s.columns[i], Value: s.values[i]})
	}
	return resources, nil
}

// Stage attaches an owned resource to its still-unpublished destination.
func (s *State) Stage(index int, ref session.ResourceRef) error {
	b := s.bindings[index]
	b.ref = ref
	return b.destination.Stage(ref, ref.Kind())
}

// MaterializeScalars resolves scalar LOB locators after response completion.
func (s *State) MaterializeScalars(ctx context.Context, op *session.Operation, resolver *catalog.Resolver) error {
	columns := s.columns
	if s.HasResources() {
		columns = slices.Clone(columns)
		for i, b := range s.bindings {
			if b.resource != 0 {
				columns[i].Type = 0
			}
		}
	}
	return materialize.New(op, resolver).LOBValues(ctx, columns, s.values, op.Config().Limits.MaxMessageBytes)
}

// Pending returns resource identities retained until output assignment.
func (s *State) Pending() []session.ResourceRef {
	var refs []session.ResourceRef
	for _, b := range s.bindings {
		if b.ref != (session.ResourceRef{}) {
			refs = append(refs, b.ref)
		}
	}
	return refs
}

// Finish reads and clears MySQL variables before permitting assignment.
func (s *State) Finish(ctx context.Context, op *session.Operation, success bool) error {
	if len(s.vars) != 0 && !op.Closed() {
		if success {
			s.err = errors.Join(s.err, s.readMySQLOutputs(ctx, op))
		}
		if err := command.InitSQL(ctx, op, []byte(s.clearSQL())); err != nil {
			op.Discard()
			return errors.Join(s.err, err)
		}
	}
	if !success {
		return nil
	}
	if s.err != nil {
		return s.err
	}
	if !s.seen {
		return fmt.Errorf("%w: output response missing", dberrors.ErrOut)
	}
	return nil
}
