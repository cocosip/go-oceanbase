package oceanbase

import (
	"context"
	"database/sql"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"io"
	"sync/atomic"
	"time"
)

// LocalInfile authorizes one Import. Pass a pointer; do not copy after use.
// Set either Reader with an exact Name, or Open to authorize the server's request name.
// Name, when nonempty, must match exactly; it is never opened as a filesystem path.
// Reader is caller-owned. Every non-nil reader returned by Open is driver-owned,
// including when Open also returns an error. Open and Read must cooperate with
// cancellation and must not recursively use the same connection.
// MaxBytes defaults to Limits.MaxLOBBytes. Timeout defaults to 30 seconds and
// bounds the whole Import, subject to synchronous Open/Read/Close returning.
// Fields and the source must not be mutated concurrently with execution.
type LocalInfile struct {
	Name     string
	Reader   io.Reader
	Open     func(context.Context, string) (io.ReadCloser, error)
	MaxBytes int64
	Timeout  time.Duration
	used     atomic.Bool
}

// ErrLocalInfile identifies refused, consumed or failed LOCAL INFILE input.
var ErrLocalInfile = dberrors.ErrLocalInfile

// Import runs a caller-supplied LOAD DATA command on the supplied DB, Conn or Tx.
// Only this command can read input; ordinary Exec/Query never authorize uploads.
// Import neither starts nor commits a transaction and never retries consumed input.
// Failure may leave partial or committed database effects; Import is not atomic.
// OceanBase imports in both modes require autocommit outside local transactions and
// XA sessions; otherwise Import returns ErrUnsupported before sending SQL/input.
// Import accepts one LOAD DATA statement, with an optional trailing semicolon.
func Import(ctx context.Context, exec Execer, query string, input *LocalInfile) (sql.Result, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	if nilExecer(exec) || query == "" {
		return nil, ErrLocalInfile
	}
	switch exec.(type) {
	case *sql.DB, *sql.Conn, *sql.Tx:
	default:
		return nil, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !input.used.CompareAndSwap(false, true) {
		return nil, ErrLocalInfile
	}
	timeout := input.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if db, ok := exec.(*sql.DB); ok {
		pinned, err := db.Conn(ctx)
		if err != nil {
			return nil, localInfileError(err)
		}
		defer func() { _ = pinned.Close() }()
		exec = pinned
	}
	result, err := exec.ExecContext(ctx, "", localInfileRequest{query: query, input: input})
	return result, localInfileError(err)
}

func (v *LocalInfile) validate() error {
	return v.source().Validate()
}

func localInfileError(err error) error {
	return streaminput.InfileError(err)
}

func (v *LocalInfile) source() *streaminput.Infile {
	if v == nil {
		return nil
	}
	return &streaminput.Infile{Name: v.Name, Reader: v.Reader, Open: v.Open, MaxBytes: v.MaxBytes, Timeout: v.Timeout, State: &v.used}
}

type localInfileRequest struct {
	query string
	input *LocalInfile
}
