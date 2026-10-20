package command_test

import (
	errors "errors"
	math "math"
	testing "testing"

	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	command "github.com/cocosip/go-oceanbase/internal/command"
)

func TestResultUnknownAndOverflow(t *testing.T) {
	if _, err := (command.Result{}).RowsAffected(); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := (command.Result{}).LastInsertId(); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := (command.Result{Known: true, ID: math.MaxUint64}).LastInsertId(); !errors.Is(err, command.ErrCountOverflow) {
		t.Fatal(err)
	}
}
