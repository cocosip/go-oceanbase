package execution_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

type argumentTimingValuer func() (driver.Value, error)

func (v argumentTimingValuer) Value() (driver.Value, error) { return v() }

type argumentTimingReader struct {
	io.Reader
	reads atomic.Int32
	bytes atomic.Int64
}

func (r *argumentTimingReader) Read(p []byte) (int, error) {
	r.reads.Add(1)
	n, err := r.Reader.Read(p)
	r.bytes.Add(int64(n))
	return n, err
}

func TestBatchArgumentResolutionTiming(t *testing.T) {
	for _, keepGoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "abort", true: "continue"}[keepGoing], func(t *testing.T) {
			const query = "INSERT INTO timing VALUES (?)"
			wantErr := errors.New("first row conversion")
			var firstCalls, futureCalls atomic.Int32
			reader := &argumentTimingReader{Reader: strings.NewReader("abc")}
			input := &oceanbase.LongData{Reader: reader, Length: 3}
			first := argumentTimingValuer(func() (driver.Value, error) {
				firstCalls.Add(1)
				if futureCalls.Load() != 0 || reader.reads.Load() != 0 {
					t.Error("future rows were resolved before the first row")
				}
				return nil, wantErr
			})
			future := argumentTimingValuer(func() (driver.Value, error) {
				futureCalls.Add(1)
				if firstCalls.Load() != 1 || reader.reads.Load() != 0 {
					t.Error("batch argument resolution did not follow row order")
				}
				return int64(42), nil
			})
			db := publictest.ConfiguredDB(t, config.Config{PrepareMode: config.PrepareServer}, func(ctx context.Context, p *wire.PacketIO) {
				serveArgumentTimingBatch(ctx, t, p, query, keepGoing, reader)
			})
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			defer func() { _ = pinned.Close() }()
			result, err := oceanbase.ExecBatch(t.Context(), pinned, query, [][]any{{first}, {future}, {input}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchSequential, ContinueOnError: keepGoing})
			checkArgumentTimingBatch(t, result, err, wantErr, firstCalls.Load(), keepGoing)
			if keepGoing {
				if futureCalls.Load() != 1 || reader.bytes.Load() != 3 {
					t.Fatalf("future calls=%d stream bytes=%d", futureCalls.Load(), reader.bytes.Load())
				}
			} else {
				if futureCalls.Load() != 0 || reader.reads.Load() != 0 {
					t.Fatalf("abort touched future rows: calls=%d reads=%d", futureCalls.Load(), reader.reads.Load())
				}
				if _, err = pinned.ExecContext(t.Context(), query, input); err != nil {
					t.Fatal("abort claimed the future stream", err)
				}
			}
			reads := reader.reads.Load()
			if _, err = pinned.ExecContext(t.Context(), query, input); !errors.Is(err, oceanbase.ErrLongData) || reader.reads.Load() != reads {
				t.Fatalf("consumed stream was reused: reads=%d error=%v", reader.reads.Load(), err)
			}
			if err = pinned.PingContext(t.Context()); err != nil {
				t.Fatal("batch conversion failure disturbed the session", err)
			}
		})
	}
}

const argumentTimingSelect = "SELECT ?,?,?"

func TestAutomaticPreparePrevalidation(t *testing.T) {
	wantValuerErr := errors.New("invalid IN conversion")
	for _, tc := range []struct {
		name       string
		mode       config.Mode
		query      string
		args       func(*oceanbase.LongData, driver.Valuer, driver.Valuer) []any
		want       error
		firstCalls int32
	}{
		{"valuer", config.ModeMySQL, argumentTimingSelect, func(input *oceanbase.LongData, first, later driver.Valuer) []any {
			return []any{first, later, input}
		}, wantValuerErr, 1},
		{"stream-before-valuer", config.ModeMySQL, argumentTimingSelect, func(input *oceanbase.LongData, first, later driver.Valuer) []any {
			return []any{input, first, later}
		}, wantValuerErr, 1},
		{"invalid-stream", config.ModeMySQL, "SELECT ?,?", func(input *oceanbase.LongData, _, later driver.Valuer) []any {
			input.Length = -2
			return []any{input, later}
		}, oceanbase.ErrLongData, 0},
		{"stream-budget", config.ModeMySQL, "SELECT ?,?", func(input *oceanbase.LongData, _, later driver.Valuer) []any {
			input.MaxBytes = 2
			return []any{input, later}
		}, wire.ErrMessageTooLarge, 0},
		{"mysql-name-before-valuer", config.ModeMySQL, argumentTimingSelect, func(input *oceanbase.LongData, first, later driver.Valuer) []any {
			return []any{sql.Named("v", first), later, input}
		}, oceanbase.ErrBind, 0},
		{"oracle-bind-count", config.ModeOracle, "SELECT :v,:missing FROM dual", func(input *oceanbase.LongData, _, _ driver.Valuer) []any {
			return []any{sql.Named("v", input)}
		}, oceanbase.ErrBind, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := byte(2)
			if tc.mode == config.ModeOracle {
				status = 6
			}
			db := publictest.ConfiguredDB(t, config.Config{Mode: tc.mode, PrepareMode: config.PrepareServer}, func(ctx context.Context, p *wire.PacketIO) {
				// Ping must be the first command after initialization: no PREPARE or upload.
				if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			defer func() { _ = pinned.Close() }()
			var firstCalls, laterCalls atomic.Int32
			first := argumentTimingValuer(func() (driver.Value, error) {
				firstCalls.Add(1)
				return nil, wantValuerErr
			})
			later := argumentTimingValuer(func() (driver.Value, error) {
				laterCalls.Add(1)
				return int64(42), nil
			})
			reader := &argumentTimingReader{Reader: strings.NewReader("abc")}
			input := &oceanbase.LongData{Reader: reader, Length: 3}
			execution, err := oceanbase.Execute(t.Context(), pinned, tc.query, tc.args(input, first, later)...)
			if execution != nil {
				_ = execution.Close()
				t.Fatal("invalid input returned a started execution", err)
			}
			if !errors.Is(err, tc.want) || firstCalls.Load() != tc.firstCalls || laterCalls.Load() != 0 || reader.reads.Load() != 0 {
				t.Fatalf("error=%v first=%d later=%d reads=%d", err, firstCalls.Load(), laterCalls.Load(), reader.reads.Load())
			}
			if err = pinned.PingContext(t.Context()); err != nil {
				t.Fatal("prevalidation disturbed the session", err)
			}
		})
	}
}

func serveArgumentTimingBatch(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, keepGoing bool, reader *argumentTimingReader) {
	t.Helper()

	if keepGoing {
		if !publictest.ServePrepare(ctx, t, p, query, 81, 1, nil) {
			return
		}
		command, err := wire.EncodeStmtExecute(81, []wire.Parameter{{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 42)}}, 4096)
		if err != nil {
			t.Error(err)
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, command) {
			return
		}
		if reader.reads.Load() != 0 {
			t.Error("future stream was read before the current row completed")
		}
		if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 81)) {
			return
		}
	}
	if !publictest.ServePrepare(ctx, t, p, query, 82, 1, nil) ||
		!testpeer.ReadCommand(ctx, t, p, append(publictest.StmtPacket(24, 82), 0, 0, 'a', 'b', 'c')) ||
		!testpeer.ServeOK(ctx, t, p, append(publictest.ExecuteHeader(82), 0, 1, 252, 0), 2) ||
		!testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 82)) ||
		!testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func checkArgumentTimingBatch(t *testing.T, result oceanbase.BatchResult, err, wantErr error, firstCalls int32, keepGoing bool) {
	t.Helper()
	completed := 0
	if keepGoing {
		completed = 2
	}
	var batchErr *oceanbase.BatchError
	if !errors.Is(err, wantErr) || !errors.As(err, &batchErr) || batchErr.Index != 0 || batchErr.Completed != completed || result.Strategy != oceanbase.BatchSequential || len(result.Items) != 3 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if firstCalls != 1 || result.Items[0].Status != oceanbase.BatchFailed || result.Items[0].Started || !errors.Is(result.Items[0].Err, wantErr) {
		t.Fatalf("first calls=%d item=%+v", firstCalls, result.Items[0])
	}
	for i, item := range result.Items[1:] {
		want := oceanbase.BatchNotExecuted
		if keepGoing {
			want = oceanbase.BatchSucceeded
		}
		if item.Status != want || item.Started != keepGoing || item.Err != nil {
			t.Fatalf("row %d=%+v", i+1, item)
		}
	}
}
