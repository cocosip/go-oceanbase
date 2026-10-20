package batch_test

import (
	"context"
	"database/sql"
	"errors"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"net"
	"testing"
	"time"
)

func TestReadTimeoutBatch(t *testing.T) {
	for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchSequential, oceanbase.BatchMultiSend} {
		t.Run(map[oceanbase.BatchStrategy]string{oceanbase.BatchSequential: "sequential", oceanbase.BatchMultiSend: "pipeline"}[strategy], func(t *testing.T) {
			configured, err := oceanbase.NewConnector(oceanbase.Config{User: publictest.TestUser, ReadTimeout: 100 * time.Millisecond, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if !publictest.ServePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, 2) {
					return
				}
				count := 1
				if strategy == oceanbase.BatchMultiSend {
					count = 3
				}
				for i := 1; i <= count; i++ {
					if !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, int64(i))) {
						return
					}
				}
				if _, e := p.ReadMessage(ctx); e == nil {
					t.Error("timed out batch continued sending")
				}
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(configured)
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := oceanbase.ExecBatch(ctx, db, testBatchSQL, [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: true})
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil || result.Strategy != strategy || len(result.Items) != 3 || result.Items[0].Status != oceanbase.BatchUnknown {
				t.Fatal("batch lost read timeout or its unknown outcome", result, err)
			}
			for _, item := range result.Items[1:] {
				want := oceanbase.BatchNotExecuted
				if strategy == oceanbase.BatchMultiSend {
					want = oceanbase.BatchUnknown
				}
				if item.Status != want {
					t.Fatal("batch invented an outcome after read timeout", result)
				}
			}
		})
	}
}
