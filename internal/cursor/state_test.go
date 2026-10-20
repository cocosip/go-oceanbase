package cursor

import (
	"context"
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
)

func TestCursorPositionBounds(t *testing.T) {
	for _, tc := range []struct {
		current int64
		pos     Position
	}{
		{1, Position{Relative, math.MinInt64}}, {math.MaxInt64, Position{}}, {1, Position{Relative, math.MaxInt64}},
	} {
		if _, err := cursorStart(tc.current, tc.pos); !errors.Is(err, dberrors.ErrCursor) {
			t.Fatal(tc, err)
		}
	}
}

func FuzzCursorPosition(f *testing.F) {
	for _, seed := range []struct{ current, offset int64 }{{0, 0}, {1, -1}, {1, math.MinInt64}, {math.MaxInt64, 1}} {
		f.Add(seed.current, seed.offset)
	}
	f.Fuzz(func(t *testing.T, current, offset int64) {
		if current < 0 {
			return
		}
		got, err := cursorStart(current, Position{Relative, offset})
		want := new(big.Int).Add(big.NewInt(current), big.NewInt(offset))
		valid := want.Sign() >= 0 && want.IsInt64()
		if valid && (err != nil || got != want.Int64()) || !valid && !errors.Is(err, dberrors.ErrCursor) {
			t.Fatal(current, offset, got, err)
		}
	})
}

func TestBusyToken(t *testing.T) {
	var token BusyToken
	if err := token.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := token.Begin(context.Background()); !errors.Is(err, dberrors.ErrCursorBusy) {
		t.Fatal(err)
	}
	token.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := token.Begin(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := token.Begin(context.Background()); err != nil {
		t.Fatal("canceled begin consumed token", err)
	}
	token.Release()
}
