package oceanbase

import (
	"errors"
	"testing"
)

// TestCursorKeyErrorsStayCursorErrors keeps cursor validation from borrowing the LOB sentinel.
func TestCursorKeyErrorsStayCursorErrors(t *testing.T) {
	if _, err := normalizeKey(map[string]any{"": int64(1)}); !errors.Is(err, errInvalidKey) || errors.Is(err, ErrLOB) {
		t.Fatal(err)
	}
	if _, err := normalizeBlobTarget(BlobTarget{Table: "t", Column: "c", Key: map[string]any{"": int64(1)}}); !errors.Is(err, ErrLOB) || !errors.Is(err, errInvalidKey) {
		t.Fatal(err)
	}
}
