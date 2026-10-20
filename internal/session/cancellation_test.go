package session

import (
	"context"
	"errors"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"testing"
	"time"
)

func TestCancellationDoesNotAcquireTargetGate(t *testing.T) {
	s := New(config.Config{}, &Negotiated{})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	target := op.ArmTarget()
	if !s.BeginCancellation() {
		t.Fatal("execution was not armed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempt, err := target.Begin(cancel)
	if err != nil {
		t.Fatal(err)
	}
	if err = attempt.BeforeSend(ctx, false); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.EndCancellation(false); close(done) }()
	select {
	case <-done:
		t.Fatal("execution ended before KILL acknowledgement")
	case <-time.After(10 * time.Millisecond):
	}
	attempt.Finish(false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("acknowledgement did not release execution")
	}
	if _, err = target.Begin(cancel); !errors.Is(err, dberrors.ErrResource) {
		t.Fatalf("late cancellation accepted: %v", err)
	}
}

func TestCancellationRetiresBeforeSend(t *testing.T) {
	s := New(config.Config{}, &Negotiated{})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	target := op.ArmTarget()
	s.BeginCancellation()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempt, err := target.Begin(cancel)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.EndCancellation(false); close(done) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("unsent attempt was not canceled")
	}
	if err = attempt.BeforeSend(context.Background(), false); !errors.Is(err, dberrors.ErrResource) {
		t.Fatalf("retired attempt sent: %v", err)
	}
	attempt.Finish(false)
	<-done
}

func TestManagedTransactionEpoch(t *testing.T) {
	s := New(config.Config{}, &Negotiated{})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	op.ManagedTransaction(true)
	before := op.Epoch()
	ok := op.Protocol().OK
	ok.Status = 1
	op.SetOK(ok)
	if op.Epoch() != before {
		t.Fatal("first managed DML invalidated transaction resources")
	}
	ok.Status = 0
	op.SetOK(ok)
	if op.Epoch() == before {
		t.Fatal("transaction end retained stale resources")
	}
}
