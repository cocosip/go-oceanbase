package call_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	call "github.com/cocosip/go-oceanbase/internal/call"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestOutTransactionCancelStopsPublication(t *testing.T) {
	txCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op := outputOperation(t)
	value, laterCalls := int64(7), 0
	args := []any{oceanbase.Out{Dest: &callbackOutScanner{scan: cancel}, Type: valueformat.TypeNameBigInt}, sql.Out{Dest: &value}, oceanbase.Out{Dest: &callbackOutScanner{scan: func() { laterCalls++ }}, Type: valueformat.TypeNameBigInt}}
	bindings := make([]*call.Binding, len(args))
	for i, arg := range args {
		binding, err := normalizeArgument(t, arg)
		if err != nil {
			t.Fatal(err)
		}
		bindings[i] = binding.(*call.Binding)
	}
	err := call.Assign(context.Background(), op, txCtx, bindings, []driver.Value{int64(42), int64(42), int64(42)})
	if !errors.Is(err, context.Canceled) || value != 7 || laterCalls != 0 {
		t.Fatal("transaction cancellation published later outputs", value, laterCalls, err)
	}
}

func TestOutCancelStopsPublication(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, framing := range []string{publictest.TestStandard, publictest.TestCompression, publictest.TestOB20} {
			t.Run(fmt.Sprintf("%v/%s", mode, framing), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				db := localInfileDB(t, mode, false, func(peerCtx context.Context, p *wire.PacketIO) {
					if !serveOutContextResponse(peerCtx, t, p, mode) {
						return
					}
					status := byte(2)
					if mode == config.ModeOracle {
						status = 6
					}
					if !testpeer.ServeOK(peerCtx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				}, func(cfg *config.Config) {
					switch framing {
					case publictest.TestCompression:
						cfg.Protocol.Compression = config.FeatureRequired
					case publictest.TestOB20:
						cfg.Protocol.OB20 = config.FeatureRequired
					}
				})
				defer func() { _ = db.Close() }()
				pinned := publictest.PinnedSession(t, db)
				defer func() { _ = pinned.Close() }()
				value, firstCalls, laterCalls := int64(7), 0, 0
				first := &callbackOutScanner{scan: func() { firstCalls++; cancel() }}
				later := &callbackOutScanner{scan: func() { laterCalls++ }}
				_, err := pinned.ExecContext(ctx, outContextQuery, oceanbase.Out{Dest: first, Type: valueformat.TypeNameBigInt}, sql.Out{Dest: &value}, oceanbase.Out{Dest: later, Type: valueformat.TypeNameBigInt})
				if !errors.Is(err, context.Canceled) || firstCalls != 1 || value != 7 || laterCalls != 0 {
					t.Errorf("canceled output publication continued: first=%d value=%d later=%d err=%v", firstCalls, value, laterCalls, err)
				}
				if err = pinned.PingContext(context.Background()); err != nil {
					t.Fatal("completed output response lost its original session", err)
				}
			})
		}
	}
}
