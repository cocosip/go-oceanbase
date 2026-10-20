package call

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
)

type stagedDestination struct {
	ref       session.ResourceRef
	published bool
}

func (d *stagedDestination) Validate() error {
	if d.published {
		return dberrors.ErrOut
	}
	return nil
}
func (d *stagedDestination) Stage(ref session.ResourceRef, _ session.ResourceKind) error {
	d.ref = ref
	return nil
}
func (d *stagedDestination) Publish() { d.published = true }

func TestResourcePublishWaitsForScanners(t *testing.T) {
	op := assignmentOperation(t)
	ref, err := op.Register(session.CursorResource)
	if err != nil {
		t.Fatal(err)
	}
	dest := new(stagedDestination)
	resource, err := NewBinding(Output{Type: "REF CURSOR"}, 163, dest)
	if err != nil {
		t.Fatal(err)
	}
	resource.ref = ref
	if err = dest.Stage(ref, ref.Kind()); err != nil {
		t.Fatal(err)
	}
	scanner := new(failingOutScanner)
	scalar, err := NewBinding(Output{Dest: scanner, Type: "VARCHAR"}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = Assign(context.Background(), op, nil, []*Binding{resource, scalar}, []driver.Value{nil, "bad"})
	var out *dberrors.OutError
	published, publishErr := op.Published(ref)
	if !errors.As(err, &out) || !out.Partial || dest.published || published || publishErr != nil {
		t.Fatalf("err=%v destination=%v ledger=%v ledgerErr=%v", err, dest.published, published, publishErr)
	}
	op.AdvanceTransaction()
	if err = Assign(context.Background(), op, nil, []*Binding{resource}, []driver.Value{nil}); !errors.Is(err, dberrors.ErrResource) || dest.published {
		t.Fatal(err, dest.published)
	}
}
