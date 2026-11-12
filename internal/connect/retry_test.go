package connect

import (
	"errors"
	"fmt"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
)

// An endpoint that is overloaded or draining must not block the remaining
// candidates; credential and database rejections must stay terminal.
func TestRetryHandshakeErrorClassifiesLoginServerErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"overloaded", &dberrors.Error{Code: 1040, SQLState: "08004"}, true},
		{"shutting down", &dberrors.Error{Code: 1053, SQLState: "08S01"}, true},
		{"user quota", fmt.Errorf("login: %w", &dberrors.Error{Code: 1203, SQLState: "08004"}), true},
		{"access denied", &dberrors.Error{Code: 1045, SQLState: "28000"}, false},
		{"database denied", &dberrors.Error{Code: 1044, SQLState: "42000"}, false},
		{"unknown database", &dberrors.Error{Code: 1049, SQLState: "42000"}, false},
		{"no sqlstate", &dberrors.Error{Code: 1040}, false},
		{"unrelated", errors.New("boom"), false},
	} {
		if got := RetryHandshakeError(tc.err); got != tc.want {
			t.Errorf("%s: RetryHandshakeError = %v, want %v", tc.name, got, tc.want)
		}
	}
}
