package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
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
var ErrLocalInfile = errors.New("oceanbase: refused or failed local infile")

// Import runs a caller-supplied LOAD DATA command on the supplied DB, Conn or Tx.
// Only this command can read input; ordinary Exec/Query never authorize uploads.
// Import neither starts nor commits a transaction and never retries consumed input.
// Failure may leave database effects; use a transaction where the server supports it.
// OceanBase MySQL-mode imports require autocommit outside local transactions and
// XA sessions; otherwise Import returns ErrUnsupported before sending SQL/input.
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

func validInfileName(name string) bool {
	return len(name) > 0 && len(name) <= 4096 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}

func nilReader(reader io.Reader) bool {
	if reader == nil {
		return true
	}
	v := reflect.ValueOf(reader)
	switch v.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

func (v *LocalInfile) validate() error {
	if v == nil || v.MaxBytes < 0 || v.Timeout < 0 || v.used.Load() || (v.Name != "" && !validInfileName(v.Name)) {
		return ErrLocalInfile
	}
	if v.Open != nil {
		if v.Reader != nil {
			return ErrLocalInfile
		}
	} else if !validInfileName(v.Name) || nilReader(v.Reader) {
		return ErrLocalInfile
	}
	return nil
}

func localInfileError(err error) error {
	if err == nil {
		return nil
	}
	// database/sql must not replay a command after a callback or source failed.
	if errors.Is(err, driver.ErrBadConn) {
		return fmt.Errorf("%w: %v", ErrLocalInfile, err)
	}
	return errors.Join(ErrLocalInfile, err)
}

type localInfileRequest struct {
	query string
	input *LocalInfile
}

type localInfileContextKey struct{}

func (c *conn) execLocalInfile(ctx context.Context, request localInfileRequest) (driver.Result, error) {
	ctx = context.WithValue(ctx, localInfileContextKey{}, request.input)
	rows, err := c.queryText(ctx, request.query)
	if err != nil {
		return nil, localInfileError(err)
	}
	if err = rows.drainAll(ctx); err != nil {
		return nil, localInfileError(err)
	}
	return rows.last, nil
}

func (r *textRows) uploadLocalInfile(ctx context.Context, request []byte) (err error) {
	v := r.localInfile
	r.localInfile = nil // A command may authorize only one server request.
	if v == nil || r.conn.session.capabilities&wire.ClientLocalFiles == 0 || len(request) < 2 || len(request) > 4097 {
		return ErrLocalInfile
	}
	encoding := r.conn.cfg.Session.Charset
	if encoding == "" {
		encoding = charsetUTF8MB4
	}
	name, err := charset.Decode(encoding, request[1:])
	if err != nil || !validInfileName(name) || v.Name != "" && v.Name != name {
		return ErrLocalInfile
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	source := v.Reader
	var owned io.ReadCloser
	defer func() {
		if owned != nil {
			err = errors.Join(err, owned.Close())
		}
		err = localInfileError(err)
	}()
	if v.Open != nil {
		owned, err = v.Open(ctx, name)
		if nilReader(owned) {
			owned = nil
			return errors.Join(ErrLocalInfile, err)
		}
		if err != nil {
			return err
		}
		source = owned
	}
	limit := v.MaxBytes
	if limit == 0 {
		limit = r.conn.cfg.Limits.MaxLOBBytes
	}
	stream := &longSource{ctx: ctx, value: &LongData{Reader: source, Length: -1}, limit: limit}
	buf := make([]byte, int(min(int64(65536), r.conn.cfg.Limits.MaxMessageBytes)))
	for {
		n, readErr := readLongChunk(stream, buf)
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if n > 0 {
			if err = r.conn.session.packets.WriteContinuation(ctx, buf[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if owned != nil {
		err = owned.Close()
		owned = nil
		if err != nil {
			return err
		}
	}
	return r.conn.session.packets.WriteContinuation(ctx, nil)
}
