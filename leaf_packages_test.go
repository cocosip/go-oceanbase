package oceanbase_test

import (
	"errors"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/metadata"
)

func TestPublicLeafAliases(t *testing.T) {
	for _, pair := range [][2]error{
		{oceanbase.ErrBind, dberrors.ErrBind},
		{oceanbase.ErrOut, dberrors.ErrOut},
		{oceanbase.ErrCursor, dberrors.ErrCursor},
		{oceanbase.ErrCursorBusy, dberrors.ErrCursorBusy},
		{oceanbase.ErrLOB, dberrors.ErrLOB},
		{oceanbase.ErrLongData, dberrors.ErrLongData},
		{oceanbase.ErrLocalInfile, dberrors.ErrLocalInfile},
		{oceanbase.ErrResource, dberrors.ErrResource},
		{oceanbase.ErrCommitUnknown, dberrors.ErrCommitUnknown},
		{oceanbase.ErrTxCleanup, dberrors.ErrTxCleanup},
		{oceanbase.ErrXA, dberrors.ErrXA},
		{oceanbase.ErrXAOutcomeUnknown, dberrors.ErrXAOutcomeUnknown},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("error identity changed: %v", pair[0])
		}
	}
	cause := errors.New("scan failed")
	var converted *dberrors.OutError
	if err := error(&oceanbase.OutError{Parameter: 2, Partial: true, Err: cause}); !errors.As(err, &converted) || !errors.Is(err, cause) || !errors.Is(err, dberrors.ErrOut) || !converted.Partial {
		t.Fatal("output error alias lost type, cause or classification", err)
	}
	var unsupported *dberrors.UnsupportedTypeError = &oceanbase.UnsupportedTypeError{Type: 255, Mode: oceanbase.ModeOracle} //nolint:staticcheck // The explicit destination type checks alias assignment compatibility.
	if unsupported.Mode != oceanbase.ModeOracle {
		t.Fatal("compatibility mode changed")
	}
	var filter metadata.MetadataFilter = oceanbase.MetadataFilter{Kind: oceanbase.MetadataColumns, NamePattern: "T"} //nolint:staticcheck // The explicit destination type checks alias assignment compatibility.
	if filter.Kind != metadata.MetadataColumns || filter.NamePattern != "T" {
		t.Fatal("metadata request changed", filter)
	}
}
