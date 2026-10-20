package session

import "github.com/cocosip/go-oceanbase/dberrors"

// ResourceKind selects the concrete manager used by lifetime cleanup.
type ResourceKind uint8

// Resource kinds do not contain feature implementation or parent statement state.
const (
	CursorResource ResourceKind = iota + 1
	LOBResource
)

// Epoch distinguishes physical connections, identities, and local transactions.
type Epoch struct {
	connection, identity, transaction uint64
}

// ResourceRef is an opaque token, comparable but meaningful only to its owning session.
type ResourceRef struct {
	owner *Session
	id    uint64
	epoch Epoch
	kind  ResourceKind
}

// Kind selects the resource manager without exposing its concrete implementation.
func (ref ResourceRef) Kind() ResourceKind { return ref.kind }

// ResourceMeta is the cleanup ledger; concrete resources and leases live in their managers.
type ResourceMeta struct {
	Ref       ResourceRef
	Published bool
}

// Register creates a resource in the current identity and transaction epochs.
func (op *Operation) Register(kind ResourceKind) (ResourceRef, error) {
	if err := op.check(); err != nil {
		return ResourceRef{}, err
	}
	if kind != CursorResource && kind != LOBResource {
		return ResourceRef{}, dberrors.ErrResource
	}
	s := op.session
	s.nextResource++
	ref := ResourceRef{owner: s, id: s.nextResource, epoch: s.epoch, kind: kind}
	s.resources[ref.id] = ResourceMeta{Ref: ref}
	return ref, nil
}

// Validate rejects foreign, forgotten, or expired resources.
func (op *Operation) Validate(ref ResourceRef) error {
	if err := op.check(); err != nil {
		return err
	}
	s := op.session
	meta, ok := s.resources[ref.id]
	if ref.owner != s || ref.epoch != s.epoch || !ok || meta.Ref != ref {
		return dberrors.ErrResource
	}
	return nil
}

// Publish retains the token across ordinary end-of-command cleanup.
func (op *Operation) Publish(ref ResourceRef) error {
	if err := op.Validate(ref); err != nil {
		return err
	}
	meta := op.session.resources[ref.id]
	meta.Published = true
	op.session.resources[ref.id] = meta
	return nil
}

// Published reports whether a validated handle is visible to its caller.
func (op *Operation) Published(ref ResourceRef) (bool, error) {
	if err := op.Validate(ref); err != nil {
		return false, err
	}
	return op.session.resources[ref.id].Published, nil
}

// Resources returns a copy so the lifetime coordinator can clean owners without sharing maps.
func (op *Operation) Resources(all bool) []ResourceMeta {
	var out []ResourceMeta
	for _, meta := range op.session.resources {
		if all || !meta.Published || meta.Ref.epoch != op.session.epoch {
			out = append(out, meta)
		}
	}
	return out
}

// Forget removes bookkeeping after owner cleanup, including a token from an expired epoch.
func (op *Operation) Forget(ref ResourceRef) {
	if ref.owner == op.session && op.session.resources[ref.id].Ref == ref {
		delete(op.session.resources, ref.id)
	}
}

// Epoch returns the current resource lifetime identity.
func (op *Operation) Epoch() Epoch { return op.session.epoch }

// StatementEpoch invalidates explicit statements after identity changes.
func (op *Operation) StatementEpoch() uint64 { return op.session.statementEpoch }

// AdvanceTransaction expires transaction resources without silently dropping cleanup records.
func (op *Operation) AdvanceTransaction() { op.session.epoch.transaction++ }

// AdvanceResources expires pooled resource handles while retaining prepared statements.
func (op *Operation) AdvanceResources() {
	op.session.epoch.identity++
	op.session.epoch.transaction++
}

// AdvanceIdentity expires resource and statement identities after a reset or change-user.
func (op *Operation) AdvanceIdentity() {
	op.session.epoch.identity++
	op.session.epoch.transaction++
	op.session.statementEpoch++
}
