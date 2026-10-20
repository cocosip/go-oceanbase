package oceanbase

import (
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"io"
	"sync/atomic"
)

// LongData is a one-use streamed IN parameter. Pass a pointer; do not copy it after use.
// Reader remains caller-owned, including on failure. It must return for cancellation to finish.
// Nil and typed-nil Readers are rejected before preparation or upload.
// Length is the exact source byte count; -1 reads to EOF. Zero requires an empty Reader.
// Text reads UTF-8 and converts to Session.Charset; otherwise bytes are binary.
// MaxBytes bounds source and converted bytes separately; zero uses Limits.MaxLOBBytes.
// Fields and Reader must not be mutated concurrently with execution.
type LongData struct {
	Reader   io.Reader
	Length   int64
	MaxBytes int64
	Text     bool
	used     atomic.Bool
}

// ErrLongData identifies invalid, consumed or failed streamed parameters.
var ErrLongData = dberrors.ErrLongData

func (v *LongData) source() *streaminput.Source {
	if v == nil {
		return nil
	}
	return &streaminput.Source{Reader: v.Reader, Length: v.Length, MaxBytes: v.MaxBytes, Text: v.Text, State: &v.used}
}
