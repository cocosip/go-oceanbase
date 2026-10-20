package valueformat

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestNumberNormalizedBudget(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"+1", "1"}, {"1.", "1"}, {"+.5e+0", "0.5"}, {"-1e0", "-1"},
		{"1.2300e2", "123.00"}, {"1e+2", "100"}, {"1e-2", "0.01"},
		{"1e" + strings.Repeat("0", 400), "1"},
	} {
		for _, limit := range []int64{int64(len(tc.want)), int64(len(tc.want)) - 1, 0, -1} {
			t.Run(fmt.Sprintf("%s/%d", tc.input, limit), func(t *testing.T) {
				got, err := PlainNumber(tc.input, limit)
				if limit < int64(len(tc.want)) {
					if !errors.Is(err, wire.ErrMessageTooLarge) {
						t.Fatal("normalized number exceeded budget", err)
					}
				} else if err != nil || got != tc.want {
					t.Fatal("fitting exact number rejected or changed", got, err)
				}
			})
		}
	}
	for _, text := range []string{"1e2147483647", "-1e-2147483648"} {
		if _, err := PlainNumber(text, 1024); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal("exponent expansion escaped budget", err)
		}
	}
}
