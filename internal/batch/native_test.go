package batch

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const nativeSQL = "INSERT INTO t VALUES (?)"
const maxInsertID = "18446744073709551615"

func nativeOperation(t *testing.T, mode config.Mode, framing string, serve func(context.Context, *wire.PacketIO)) (context.Context, *session.Operation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	cfg, err := config.Normalize(config.Config{User: testpeer.User, Mode: mode, AllowMultiStatements: true})
	if err != nil {
		t.Fatal(err)
	}
	clientPackets, peerPackets := wire.NewPacketIO(client, 0), wire.NewPacketIO(peer, 0)
	for _, packets := range []*wire.PacketIO{clientPackets, peerPackets} {
		switch framing {
		case testpeer.Compression:
			err = packets.EnableCompression()
		case testpeer.OB20:
			err = packets.EnableOB20(wire.OB20Options{ConnectionID: 42})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	status := uint16(2)
	if mode == config.ModeOracle {
		status = 5
	}
	s := session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: clientPackets, Mode: mode, OBVersion: "4.3.5.0", Capabilities: wire.ClientProtocol41 | wire.ClientMultiStatements, OK: wire.OK{Status: status}})
	op, err := s.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	go func() { defer func() { _ = peer.Close() }(); serve(ctx, peerPackets) }()
	return ctx, op
}

func nativeRequest(t *testing.T, rows [][]any, options Options) Request {
	t.Helper()
	options, err := NormalizeOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	return Request{Query: nativeSQL, RawRows: rows, Rows: rows, Options: options, Result: &Result{Items: make([]Item, len(rows)), Strategy: Sequential}, Outcome: &NativeOutcome{}}
}

func serveNativePrepare(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte) bool {
	t.Helper()
	return testpeer.ReadCommand(ctx, t, p, append([]byte{22}, nativeSQL...)) && testpeer.SendPackets(ctx, t, p, []byte{0, 7, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0}, testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, status, 0})
}

func nativeOK(affected, id uint64, status byte) []byte {
	b := wire.AppendComplexLenEnc([]byte{0}, affected)
	b = wire.AppendComplexLenEnc(b, id)
	return append(b, status, 0, 3, 0)
}

func nativeExecute(t *testing.T, value uint64) []byte {
	t.Helper()
	payload, err := wire.EncodeStmtExecute(7, []wire.Parameter{{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, value)}}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestNativePipelineAcknowledgements(t *testing.T) {
	ctx, op := nativeOperation(t, config.ModeMySQL, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		if !serveNativePrepare(ctx, t, p, 2) {
			return
		}
		for i := uint64(1); i <= 3; i++ {
			if !testpeer.ReadCommand(ctx, t, p, nativeExecute(t, i)) {
				return
			}
			response := nativeOK(i, math.MaxUint64, 2)
			if i == 2 {
				response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
			}
			if !testpeer.SendPackets(ctx, t, p, response) {
				return
			}
		}
		testpeer.ReadCommand(ctx, t, p, []byte{25, 7, 0, 0, 0})
	})
	request := nativeRequest(t, [][]any{{1}, {2}, {3}}, Options{ContinueOnError: true})
	handled, err := TryNative(ctx, op, nil, nil, nil, request, false)
	var batchErr *dberrors.BatchError
	if !handled || !errors.As(err, &batchErr) || batchErr.Index != 1 || batchErr.Completed != 2 || request.Result.Strategy != MultiSend {
		t.Fatal(handled, request.Result, err)
	}
	if request.Result.Items[0].InsertID != maxInsertID || request.Result.Items[1].Status != Failed || request.Result.Items[2].RowsAffected != 3 || !request.Outcome.Started || request.Outcome.WarningsChanged || op.Closed() || op.ResponseActive() {
		t.Fatal(request.Result, request.Outcome, op.Closed())
	}
}

func TestNativeArrayDiagnosticsAndAggregate(t *testing.T) {
	for _, framing := range []string{testpeer.Standard, testpeer.Compression, testpeer.OB20} {
		t.Run(framing, func(t *testing.T) {
			ctx, op := nativeOperation(t, config.ModeOracle, framing, func(ctx context.Context, p *wire.PacketIO) {
				if !serveNativePrepare(ctx, t, p, 5) {
					return
				}
				rows := [][]wire.Parameter{{{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 1)}}, {{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 2)}}}
				payload, err := wire.EncodeStmtExecuteArray(7, rows, 1<<20, wire.ExecuteOptions{})
				if err != nil {
					t.Error(err)
					return
				}
				if !testpeer.ReadCommand(ctx, t, p, payload) {
					return
				}
				diagnostic := binary.LittleEndian.AppendUint64([]byte{0, 0}, 1)
				diagnostic = binary.LittleEndian.AppendUint64(diagnostic, 1062)
				diagnostic = append(diagnostic, 3, 'd', 'u', 'p')
				if testpeer.SendPackets(ctx, t, p, []byte{3}, testpeer.TextColumn("sql_no", 8, 63, 0), testpeer.TextColumn("error_code", 8, 63, 0), testpeer.TextColumn("error_message", 253, 45, 0), []byte{254, 0, 0, 13, 0}, diagnostic, []byte{254, 0, 0, 13, 0}, nativeOK(1, 17, 5)) {
					testpeer.ReadCommand(ctx, t, p, []byte{25, 7, 0, 0, 0})
				}
			})
			request := nativeRequest(t, [][]any{{1}, {2}}, Options{Strategy: ArrayBinding, ContinueOnError: true})
			handled, err := TryNative(ctx, op, nil, nil, nil, request, true)
			var batchErr *dberrors.BatchError
			if !handled || !errors.As(err, &batchErr) || batchErr.Index != 1 || batchErr.Completed != 1 || request.Result.Strategy != ArrayBinding || request.Result.Items[0].Status != Succeeded || request.Result.Items[0].RowsAffectedKnown || request.Result.Items[0].InsertIDKnown || request.Result.Items[1].Status != Failed || !request.Outcome.WarningsKnown || request.Outcome.WarningCount != 3 || op.Closed() || op.ResponseActive() {
				t.Fatal(request.Result, request.Outcome, err)
			}
		})
	}
}

func serveRewriteGuard(ctx context.Context, t *testing.T, p *wire.PacketIO) bool {
	t.Helper()
	query := "SELECT MAX(COALESCE(LOWER(VALUE),'')!='false') FROM oceanbase.GV$OB_PARAMETERS WHERE NAME='ob_enable_batched_multi_statement'"
	return testpeer.ReadQuery(ctx, t, p, query) && testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '0'}, []byte{254, 0, 0, 2, 0})
}

func TestNativeRewritePreservesConfirmedPrefix(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "server", true: "disconnect"}[broken], func(t *testing.T) {
			ctx, op := nativeOperation(t, config.ModeMySQL, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveRewriteGuard(ctx, t, p) || !testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 )\n;\nINSERT INTO t VALUES ( 2 )\n;\nINSERT INTO t VALUES ( 3 )") || !testpeer.SendPackets(ctx, t, p, nativeOK(1, 7, 10)) {
					return
				}
				if !broken {
					testpeer.SendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'})
				}
			})
			request := nativeRequest(t, [][]any{{1}, {2}, {3}}, Options{Strategy: Rewrite})
			handled, err := TryNative(ctx, op, nil, nil, nil, request, false)
			if !handled || err == nil || request.Result.Items[0].Status != Succeeded || request.Result.Items[0].InsertID != "7" || !request.Result.Items[2].Started {
				t.Fatal(request.Result, err)
			}
			if broken {
				if request.Result.Items[1].Status != Unknown || request.Result.Items[2].Status != Unknown || !op.Closed() {
					t.Fatal(request.Result, err)
				}
			} else if request.Result.Items[1].Status != Failed || request.Result.Items[2].Status != NotExecuted || op.Closed() {
				t.Fatal(request.Result, err)
			}
			if op.ResponseActive() {
				t.Fatal("response boundary retained")
			}
		})
	}
}

func TestNativeMultiValuesAggregate(t *testing.T) {
	for _, framing := range []string{testpeer.Standard, testpeer.Compression, testpeer.OB20} {
		t.Run(framing, func(t *testing.T) {
			ctx, op := nativeOperation(t, config.ModeMySQL, framing, func(ctx context.Context, p *wire.PacketIO) {
				if testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 ),( 2 )") {
					testpeer.SendPackets(ctx, t, p, nativeOK(2, math.MaxUint64, 2))
				}
			})
			request := nativeRequest(t, [][]any{{1}, {2}}, Options{Strategy: MultiValues})
			handled, err := TryNative(ctx, op, nil, nil, nil, request, false)
			if !handled || err != nil || len(request.Result.Chunks) != 1 || request.Result.Chunks[0].InsertID != maxInsertID || request.Result.Items[0].RowsAffectedKnown || request.Result.Items[1].InsertIDKnown || request.Result.Strategy != MultiValues || !request.Outcome.WarningsKnown || op.ResponseActive() {
				t.Fatal(request.Result, err)
			}
		})
	}
}

type untouchedArgument struct{ calls *int }

func (a untouchedArgument) Resolve() (any, error)        { *a.calls++; return int64(1), nil }
func (a untouchedArgument) Value() (driver.Value, error) { *a.calls++; return int64(1), nil }

func TestNativeQualificationDoesNotResolve(t *testing.T) {
	for _, strategy := range []Strategy{Auto, ArrayBinding, Rewrite, MultiValues} {
		var calls int
		s := session.New(config.Config{PrepareMode: config.PrepareServer}, &session.Negotiated{})
		op, err := s.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		request := nativeRequest(t, [][]any{{untouchedArgument{&calls}}, {1}}, Options{Strategy: strategy, ContinueOnError: strategy == Auto || strategy == ArrayBinding})
		request.Rows = [][]any{{sql.Named("a", untouchedArgument{&calls})}, {untouchedArgument{&calls}}}
		handled, err := TryNative(context.Background(), op, nil, nil, nil, request, true)
		op.Release()
		if handled || err != nil || calls != 0 {
			t.Fatal(strategy, handled, calls, err)
		}
	}
}
