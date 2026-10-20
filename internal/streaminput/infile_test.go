package streaminput

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
)

func TestLocalInfileValidation(t *testing.T) {
	for _, input := range []*Infile{
		nil, {}, {Name: testInfileName}, {Reader: strings.NewReader("x")},
		{Name: "a\x00b", Reader: strings.NewReader("x")}, {Name: "\xff", Reader: strings.NewReader("x")},
		{Name: strings.Repeat("x", 4097), Reader: strings.NewReader("x")},
		{Name: testInfileName, Reader: (*bytes.Reader)(nil)},
		{Name: testInfileName, Reader: strings.NewReader("x"), MaxBytes: -1},
		{Name: testInfileName, Reader: strings.NewReader("x"), Timeout: -1},
		{Name: testInfileName, Reader: strings.NewReader("x"), Open: func(context.Context, string) (io.ReadCloser, error) {
			t.Fatal("invalid source opened")
			return nil, nil
		}},
	} {
		if input != nil {
			input.State = new(atomic.Bool)
		}
		if err := input.Validate(); !errors.Is(err, dberrors.ErrLocalInfile) {
			t.Fatal("invalid input accepted", err)
		}
	}
	state := new(atomic.Bool)
	input := Infile{Name: testInfileName, Reader: strings.NewReader("x"), State: state}
	if err := input.Validate(); err != nil || state.Load() {
		t.Fatal(err)
	}
	if !input.Claim() || input.Claim() || !errors.Is(input.Validate(), dberrors.ErrLocalInfile) {
		t.Fatal("claim state")
	}
	if err := InfileError(driver.ErrBadConn); !errors.Is(err, dberrors.ErrLocalInfile) || errors.Is(err, driver.ErrBadConn) {
		t.Fatal("replayable error", err)
	}
}

const testInfileName = "input.csv"
