package lifetime

import (
	"context"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
)

func lifetimeOperation(t *testing.T) *session.Operation {
	t.Helper()
	cfg, err := config.Normalize(config.Config{User: "app", Mode: config.ModeMySQL})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(cfg, &session.Negotiated{Mode: config.ModeMySQL})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return op
}

func TestCloseRejectsUnknownKind(t *testing.T) {
	op := lifetimeOperation(t)
	var cursors cursor.Manager
	var lobs lob.Manager
	if err := Close(context.Background(), op, &cursors, &lobs, session.ResourceRef{}); !errors.Is(err, dberrors.ErrResource) {
		t.Fatalf("close of zero ref = %v, want ErrResource", err)
	}
}

// Closing must forget the token even when its manager holds no concrete record.
func TestCloseForgetsToken(t *testing.T) {
	op := lifetimeOperation(t)
	ref, err := op.Register(session.CursorResource)
	if err != nil {
		t.Fatal(err)
	}
	var cursors cursor.Manager
	var lobs lob.Manager
	if err = Close(context.Background(), op, &cursors, &lobs, ref); err != nil {
		t.Fatalf("close = %v", err)
	}
	if err = op.Validate(ref); !errors.Is(err, dberrors.ErrResource) {
		t.Fatalf("token still tracked: %v", err)
	}
}

// A retained resource survives targeted cleanup and is retired by a full pass.
func TestCleanupHonorsRetainedResources(t *testing.T) {
	op := lifetimeOperation(t)
	ref, err := op.Register(session.CursorResource)
	if err != nil {
		t.Fatal(err)
	}
	var cursors cursor.Manager
	var lobs lob.Manager
	if err = Cleanup(context.Background(), op, &cursors, &lobs, false, []session.ResourceRef{ref}); err != nil {
		t.Fatalf("retained cleanup = %v", err)
	}
	if err = op.Validate(ref); err != nil {
		t.Fatalf("retained resource retired: %v", err)
	}
	if err = Cleanup(context.Background(), op, &cursors, &lobs, false, nil); err != nil {
		t.Fatalf("cleanup = %v", err)
	}
	if err = op.Validate(ref); !errors.Is(err, dberrors.ErrResource) {
		t.Fatalf("unretained resource survived: %v", err)
	}
}

// Published handles are visible to the caller, so only a full pass retires them.
func TestCleanupAllRetiresPublishedResources(t *testing.T) {
	op := lifetimeOperation(t)
	ref, err := op.Register(session.LOBResource)
	if err != nil {
		t.Fatal(err)
	}
	if err = op.Publish(ref); err != nil {
		t.Fatal(err)
	}
	var cursors cursor.Manager
	var lobs lob.Manager
	if err = Cleanup(context.Background(), op, &cursors, &lobs, false, nil); err != nil {
		t.Fatalf("bounded cleanup = %v", err)
	}
	if err = op.Validate(ref); err != nil {
		t.Fatalf("published handle retired early: %v", err)
	}
	if err = Cleanup(context.Background(), op, &cursors, &lobs, true, nil); err != nil {
		t.Fatalf("full cleanup = %v", err)
	}
	if err = op.Validate(ref); !errors.Is(err, dberrors.ErrResource) {
		t.Fatalf("published handle survived full cleanup: %v", err)
	}
}

// A full pass retires every tracked resource, not only the first one.
func TestCleanupRetiresEveryResource(t *testing.T) {
	op := lifetimeOperation(t)
	first, err := op.Register(session.CursorResource)
	if err != nil {
		t.Fatal(err)
	}
	second, err := op.Register(session.CursorResource)
	if err != nil {
		t.Fatal(err)
	}
	var cursors cursor.Manager
	var lobs lob.Manager
	if err = Cleanup(context.Background(), op, &cursors, &lobs, true, nil); err != nil {
		t.Fatalf("cleanup = %v", err)
	}
	for _, ref := range []session.ResourceRef{first, second} {
		if err = op.Validate(ref); !errors.Is(err, dberrors.ErrResource) {
			t.Fatalf("resource survived cleanup: %v", err)
		}
	}
}
