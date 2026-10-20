package session

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
)

func TestGateCancellationAndDiscard(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	s := New(config.Config{}, &Negotiated{Raw: client})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting acquisition: %v", err)
	}
	finished := make(chan struct{})
	go func() { s.Discard(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("discard waited for the occupied protocol gate")
	}
	op.Release()
	op.Release()
	if _, err = s.Acquire(context.Background()); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("discarded session acquired: %v", err)
	}
}

func TestResourceEpochsAndOwnership(t *testing.T) {
	s := New(config.Config{}, &Negotiated{})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	ref, err := op.Register(CursorResource)
	if err != nil {
		t.Fatal(err)
	}
	if err = op.Publish(ref); err != nil {
		t.Fatal(err)
	}
	if len(op.Resources(false)) != 0 || len(op.Resources(true)) != 1 {
		t.Fatal("published resource was selected for ordinary cleanup")
	}
	other := New(config.Config{}, &Negotiated{})
	otherOp, err := other.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer otherOp.Release()
	if err = otherOp.Validate(ref); !errors.Is(err, dberrors.ErrResource) {
		t.Fatalf("foreign session accepted resource: %v", err)
	}
	op.AdvanceTransaction()
	if err = op.Validate(ref); !errors.Is(err, dberrors.ErrResource) {
		t.Fatalf("stale transaction resource accepted: %v", err)
	}
	// Expired IDs remain identifiable for owner cleanup; advancing an epoch is not CLOSE.
	if len(op.Resources(true)) != 1 {
		t.Fatal("epoch change lost the cleanup record")
	}
	op.Forget(ref)
	if len(op.Resources(true)) != 0 {
		t.Fatal("forgotten resource retained")
	}
}
