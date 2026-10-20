package batch

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"net"
	"sync/atomic"
	"testing"
)

type batchCancelReadConn struct {
	net.Conn
	response []byte
	cancel   func()
}

func (c *batchCancelReadConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if bytes.Equal(b[:n], c.response) {
		c.cancel()
	}
	return n, err
}

type batchLateWriteConn struct {
	net.Conn
	command, response []byte
	read              chan struct{}
	err               error
}

func (c *batchLateWriteConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if bytes.Equal(b[:n], c.response) {
		close(c.read)
	}
	return n, err
}

func (c *batchLateWriteConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if err == nil && bytes.Equal(b, c.command) {
		<-c.read
		return n, c.err
	}
	return n, err
}

type longQueuedContext struct {
	context.Context
	done          chan struct{}
	canceled      atomic.Bool
	registrations atomic.Int32
	cancelAt      int32
}

func (c *longQueuedContext) Done() <-chan struct{} { return c.done }
func (c *longQueuedContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}
func (c *longQueuedContext) AfterFunc(func()) func() bool {
	if c.registrations.Add(1) == c.cancelAt {
		c.cancel()
	}
	return func() bool { return true }
}
func (c *longQueuedContext) cancel() {
	if c.canceled.CompareAndSwap(false, true) {
		close(c.done)
	}
}

func TestBatchCancellationPreservesPriorError(t *testing.T) {
	ctx := &longQueuedContext{Context: context.Background(), done: make(chan struct{})}
	_, op := nativeOperation(t, config.ModeMySQL, testpeer.Standard, func(peerctx context.Context, p *wire.PacketIO) {
		if !serveNativePrepare(peerctx, t, p, 2) || !testpeer.ReadCommand(peerctx, t, p, nativeExecuteSigned(t, 1)) || !testpeer.SendPackets(peerctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}) || !testpeer.ReadCommand(peerctx, t, p, nativeExecuteSigned(t, 2)) || !testpeer.SendPackets(peerctx, t, p, commandPacket(17, 2)) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(peerctx); err == nil {
			t.Error("canceled batch retained connection")
		}
	})
	raw := &batchCancelReadConn{Conn: op.Protocol().Conn, response: commandPacket(17, 2), cancel: ctx.cancel}
	if err := op.Packets().ReplaceConn(raw); err != nil {
		t.Fatal(err)
	}
	request := nativeRequest(t, [][]any{{1}, {2}}, Options{ContinueOnError: true, ChunkRows: 1, ChunkParameters: 2})
	_, err := TryNative(ctx, op, nil, nil, nil, request, false)
	r := request.Result
	var server *dberrors.Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &server) || r.Items[1].Status != Succeeded || !op.Closed() {
		t.Fatal(r, err, !op.Closed())
	}
}

func TestBatchLateWritePreservesServerError(t *testing.T) {
	response := []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
	_, op := nativeOperation(t, config.ModeMySQL, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		if !serveNativePrepare(ctx, t, p, 2) || !testpeer.ReadCommand(ctx, t, p, nativeExecuteSigned(t, 1)) || !testpeer.SendPackets(ctx, t, p, commandPacket(1, 2)) || !testpeer.ReadCommand(ctx, t, p, nativeExecuteSigned(t, 2)) {
			return
		}
		_ = testpeer.SendPackets(ctx, t, p, response)
	})
	late := errors.New("late write failure")
	raw := &batchLateWriteConn{Conn: op.Protocol().Conn, command: nativeExecuteSigned(t, 2), response: response, read: make(chan struct{}), err: late}
	if err := op.Packets().ReplaceConn(raw); err != nil {
		t.Fatal(err)
	}
	request := nativeRequest(t, [][]any{{1}, {2}}, Options{ContinueOnError: true, ChunkRows: 2, ChunkParameters: 2})
	_, err := TryNative(context.Background(), op, nil, nil, nil, request, false)
	r := request.Result
	var server *dberrors.Error
	if !errors.Is(err, late) || !errors.As(err, &server) || !errors.As(r.Items[1].Err, &server) || r.Items[1].Status != Failed {
		t.Fatal(r, err)
	}
}

func TestBatchArrayResponseBudget(t *testing.T) {
	for _, deprecated := range []bool{false, true} {
		for _, oversize := range []bool{false, true} {
			t.Run(fmt.Sprint(deprecated, "/", oversize), func(t *testing.T) {
				packets := append(arrayDiagnostics(arrayDiagnostic(1, 1062, "duplicate")), commandPacket(1, 5))
				var size int64
				for _, packet := range packets {
					size += int64(len(packet))
				}
				capabilities := uint64(0)
				if deprecated {
					capabilities = wire.ClientDeprecateEOF
				}
				_, op := nativeOperation(t, config.ModeOracle, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.ReadCommand(ctx, t, p, arrayPacket(t, []any{1, 2}, wire.ExecuteOptions{})) {
						return
					}
					for _, packet := range packets {
						if p.WriteMessage(ctx, packet) != nil {
							return
						}
					}
				})
				defer op.Discard()
				p := op.Protocol()
				p.Capabilities = capabilities
				op.SetIdentity(p)
				cfg := op.Config()
				cfg.Limits.MaxMessageBytes = size
				if oversize {
					cfg.Limits.MaxMessageBytes--
				}
				op.SetConfig(cfg)
				items := make([]Item, 2)
				n := native{op: op, outcome: new(NativeOutcome)}
				err := n.runBatchArrayLocked(context.Background(), arrayPacket(t, []any{1, 2}, wire.ExecuteOptions{}), items)
				if oversize {
					if !errors.Is(err, wire.ErrMessageTooLarge) || items[0].Status != Unknown || items[1].Status != Unknown || !op.Closed() {
						t.Fatalf("response budget: %+v %v", items, err)
					}
				} else if err != nil || items[0].Status != Succeeded || items[1].Status != Failed {
					t.Fatalf("complete native response: %+v %v", items, err)
				}
			})
		}
	}
}

func TestBatchArrayCancellation(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(fmt.Sprint(acknowledged), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, op := nativeOperation(t, config.ModeOracle, testpeer.Standard, func(peerCtx context.Context, p *wire.PacketIO) {
				if !serveNativePrepare(peerCtx, t, p, 5) || !testpeer.ReadCommand(peerCtx, t, p, arrayPacket(t, []any{1, 2}, wire.ExecuteOptions{})) {
					return
				}
				if acknowledged {
					if !serveArrayDiagnostics(peerCtx, t, p, arrayDiagnostic(1, 1062, "duplicate")) || !testpeer.SendPackets(peerCtx, t, p, commandPacket(17, 5)) {
						return
					}
				} else {
					cancel()
				}
				p.ResetSequence()
				if _, err := p.ReadMessage(peerCtx); err == nil {
					t.Error("canceled array batch sent another command")
				}
			})
			if acknowledged {
				if err := op.Packets().ReplaceConn(&batchCancelReadConn{Conn: op.Protocol().Raw, response: commandPacket(17, 5), cancel: cancel}); err != nil {
					t.Fatal(err)
				}
			}
			request := nativeRequest(t, [][]any{{1}, {2}, {3}}, Options{Strategy: ArrayBinding, ContinueOnError: true, ChunkRows: 2})
			_, err := TryNative(ctx, op, nil, nil, nil, request, true)
			r := request.Result
			if !errors.Is(err, context.Canceled) || r.Items[2].Status != NotExecuted || r.Items[2].Started {
				t.Fatalf("cancellation: %+v %v", r, err)
			}
			if acknowledged {
				var server *dberrors.Error
				if r.Items[0].Status != Succeeded || r.Items[1].Status != Failed || !errors.As(err, &server) || server.Code != 1062 {
					t.Fatalf("acknowledged error lost: %+v %v", r, err)
				}
			} else if r.Items[0].Status != Unknown || r.Items[1].Status != Unknown {
				t.Fatalf("unacknowledged cancellation: %+v %v", r, err)
			}
		})
	}
}

func TestBatchRewriteAcknowledgedErrorAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, op := nativeOperation(t, config.ModeMySQL, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !testpeer.ReadQuery(ctx, t, p, rewritePair) {
			return
		}
		_ = testpeer.SendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'})
	})
	if err := op.Packets().ReplaceConn(&batchCancelReadConn{Conn: op.Protocol().Raw, response: []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}, cancel: cancel}); err != nil {
		t.Fatal(err)
	}
	request := nativeRequest(t, [][]any{{1}, {2}}, Options{Strategy: Rewrite})
	_, err := TryNative(ctx, op, nil, nil, nil, request, false)
	r := request.Result
	var server *dberrors.Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &server) || server.Code != 1062 || r.Items[0].Status != Failed || r.Items[1].Status != NotExecuted {
		t.Fatalf("acknowledged server error or cancellation lost: %+v %v", r, err)
	}
}

const rewritePair = "INSERT INTO t VALUES ( 1 )\n;\nINSERT INTO t VALUES ( 2 )"

func commandPacket(affected uint64, status byte) []byte {
	data := binary.LittleEndian.AppendUint64([]byte{254}, affected)
	data = binary.LittleEndian.AppendUint64(append(data, 254), 0)
	return append(append([]byte{0}, data...), status, 0, 0, 0)
}
func nativeExecuteSigned(t *testing.T, value int64) []byte { return nativeExecute(t, uint64(value)) }
func arrayPacket(t testing.TB, values []any, options wire.ExecuteOptions) []byte {
	t.Helper()
	rows := make([][]wire.Parameter, len(values))
	for i, value := range values {
		normalized, _, err := parameters.NormalizeStep(value)
		if err != nil {
			t.Fatal(err)
		}
		parameter, err := valuecodec.EncodeParameter(normalized, config.ModeOracle, config.SessionOptions{}, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		rows[i] = []wire.Parameter{parameter}
	}
	packet, err := wire.EncodeStmtExecuteArray(7, rows, 1<<20, options)
	if err != nil {
		t.Fatal(err)
	}
	return packet
}
func serveArrayDiagnostics(ctx context.Context, t *testing.T, p *wire.PacketIO, rows ...[]byte) bool {
	t.Helper()
	return testpeer.SendPackets(ctx, t, p, arrayDiagnostics(rows...)...)
}
func serveBatchRewriteGuard(ctx context.Context, t testing.TB, p *wire.PacketIO, value string, status byte) bool {
	t.Helper()
	query := "SELECT MAX(COALESCE(LOWER(VALUE),'')!='false') FROM oceanbase.GV$OB_PARAMETERS WHERE NAME='ob_enable_batched_multi_statement'"
	return testpeer.ReadQuery(ctx, t, p, query) && testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("server-merge-on", 253, 45, 0), []byte{254, 0, 0, status, 0}, append([]byte{byte(len(value))}, value...), []byte{254, 0, 0, status, 0})
}
