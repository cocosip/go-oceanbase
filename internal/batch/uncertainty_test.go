package batch

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const rewriteEarlyEnd = "early-end"
const rewriteUnexpectedMore = "unexpected-more"

func arrayDiagnostic(position, code uint64, message string) []byte {
	data := binary.LittleEndian.AppendUint64([]byte{0, 0}, position)
	data = binary.LittleEndian.AppendUint64(data, code)
	return append(append(data, byte(len(message))), message...)
}

func arrayDiagnostics(rows ...[]byte) [][]byte {
	packets := [][]byte{{3}, testpeer.TextColumn("sql_no", 8, 45, 0), testpeer.TextColumn("error_code", 8, 45, 0), testpeer.TextColumn("error_message", 253, 45, 0), {254, 0, 0, 13, 0}}
	packets = append(packets, rows...)
	return append(packets, []byte{254, 0, 0, 13, 0})
}

func arrayUncertaintyPackets(failure string) [][]byte {
	row := arrayDiagnostic(1, 1062, "duplicate")
	var packets [][]byte
	switch failure {
	case "array-disconnect":
		return nil
	case "top-error":
		packets = [][]byte{{255, 38, 4, '#', 'H', 'Y', '0', '0', '0', 'e'}}
	case "duplicate":
		packets = arrayDiagnostics(row, row)
	case "position":
		packets = arrayDiagnostics(arrayDiagnostic(2, 1062, "out of range"))
	case "code":
		packets = arrayDiagnostics(arrayDiagnostic(1, math.MaxUint64, "bad code"))
	case "array-null":
		packets = arrayDiagnostics([]byte{0, 28})
	case "metadata":
		packets = [][]byte{{1}, testpeer.TextColumn("wrong", 253, 45, 0), {254, 0, 0, 13, 0}}
	case "array-row-error":
		packets = arrayDiagnostics(row)
		packets[len(packets)-1] = []byte{255, 38, 4, '#', 'H', 'Y', '0', '0', '0', 'e'}
	case "array-metadata-error", "array-metadata-eof-error":
		packets = arrayDiagnostics(row)
		index := 2
		if strings.Contains(failure, "eof") {
			index = 4
		}
		packets = append(packets[:index], []byte{255, 38, 4, '#', 'H', 'Y', '0', '0', '0', 'e'})
	case "metadata-out":
		packets = append(arrayDiagnostics(row), nativeOK(1, 0, 5))
		packets[3][len(packets[3])-1] = 1
	case "array-tail-error":
		packets = append(arrayDiagnostics(row), []byte{255, 38, 4, '#', 'H', 'Y', '0', '0', '0', 'e'})
	case "array-extra":
		packets = [][]byte{nativeOK(2, 0, 13)}
	}
	return packets
}

func TestBatchArrayUncertainResponse(t *testing.T) {
	for _, failure := range []string{"array-disconnect", "top-error", "duplicate", "position", "code", "array-null", "array-tail-error", "array-extra", "metadata", "metadata-out", "array-row-error", "array-metadata-error", "array-metadata-eof-error"} {
		t.Run(failure, func(t *testing.T) {
			ctx, op := nativeOperation(t, config.ModeOracle, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveNativePrepare(ctx, t, p, 5) {
					return
				}
				p.ResetSequence()
				if _, err := p.ReadMessage(ctx); err != nil {
					t.Error(err)
					return
				}
				packets := arrayUncertaintyPackets(failure)
				for _, packet := range packets {
					if p.WriteMessage(ctx, packet) != nil {
						return
					}
				}
			})
			request := nativeRequest(t, [][]any{{1}, {2}, {3}}, Options{Strategy: ArrayBinding, ContinueOnError: true, ChunkRows: 2})
			handled, err := TryNative(ctx, op, nil, nil, nil, request, true)
			r := request.Result
			if !handled || err == nil || r.Strategy != ArrayBinding || r.Items[0].Status != Unknown || r.Items[1].Status != Unknown || r.Items[2].Status != NotExecuted || r.Items[2].Started || !op.Closed() || op.ResponseActive() {
				t.Fatalf("uncertainty: %+v %v", r, err)
			}
			if strings.HasSuffix(failure, "error") {
				var server *dberrors.Error
				if !errors.As(err, &server) || server.Code != 1062 {
					t.Fatal("server error during diagnostics lost", err)
				}
			}
		})
	}
}

func TestBatchRewriteUncertainty(t *testing.T) {
	for _, name := range []string{"peer-closed", "short-ok", "rowset", rewriteEarlyEnd, rewriteUnexpectedMore} {
		t.Run(name, func(t *testing.T) {
			ctx, op := nativeOperation(t, config.ModeMySQL, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveRewriteGuard(ctx, t, p) || !testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 )\n;\nINSERT INTO t VALUES ( 2 )\n;\nINSERT INTO t VALUES ( 3 )") {
					return
				}
				if name == rewriteEarlyEnd {
					testpeer.SendPackets(ctx, t, p, nativeOK(3, 0, 2))
					return
				}
				if !testpeer.SendPackets(ctx, t, p, nativeOK(1, 0, 10)) {
					return
				}
				switch name {
				case "short-ok":
					testpeer.SendPackets(ctx, t, p, []byte{0})
				case "rowset":
					testpeer.SendPackets(ctx, t, p, []byte{1})
				case rewriteUnexpectedMore:
					testpeer.SendPackets(ctx, t, p, nativeOK(1, 0, 10), nativeOK(1, 0, 10))
				}
			})
			request := nativeRequest(t, [][]any{{1}, {2}, {3}, {4}}, Options{Strategy: Rewrite, ChunkRows: 3})
			handled, err := TryNative(ctx, op, nil, nil, nil, request, false)
			r := request.Result
			var batchErr *dberrors.BatchError
			if !handled || !errors.As(err, &batchErr) || r.Items[3].Status != NotExecuted || r.Items[3].Started {
				t.Fatal(r, err)
			}
			acknowledged := 1
			switch name {
			case rewriteEarlyEnd:
				acknowledged = 0
			case rewriteUnexpectedMore:
				acknowledged = 2
			}
			if batchErr.Completed != acknowledged || batchErr.Index != acknowledged {
				t.Fatal(r, err)
			}
			for i := acknowledged; i < 3; i++ {
				if r.Items[i].Status != Unknown || !r.Items[i].Started {
					t.Fatal(r)
				}
			}
			if name == "peer-closed" {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, wire.ErrMalformed) {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeCancellationRetiresResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, op := nativeOperation(t, config.ModeMySQL, testpeer.Standard, func(peerCtx context.Context, p *wire.PacketIO) {
		if testpeer.ReadQuery(peerCtx, t, p, "INSERT INTO t VALUES ( 1 ),( 2 )") {
			cancel()
			_, _ = p.ReadMessage(peerCtx)
		}
	})
	request := nativeRequest(t, [][]any{{1}, {2}, {3}}, Options{Strategy: MultiValues, ChunkRows: 2})
	handled, err := TryNative(ctx, op, nil, nil, nil, request, false)
	if !handled || !errors.Is(err, context.Canceled) || request.Result.Items[0].Status != Unknown || request.Result.Items[1].Status != Unknown || request.Result.Items[2].Started || !op.Closed() || op.ResponseActive() {
		t.Fatal(request.Result, err)
	}
}
