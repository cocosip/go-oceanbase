//go:build windows

package batch_test

import (
	"context"
	"database/sql"
	"fmt"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"net"
	"testing"
)

func sharedMemoryDB(t *testing.T, cfg oceanbase.Config, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	return sharedMemoryDBConn(t, cfg, func(ctx context.Context, _ net.Conn, p *wire.PacketIO) { serve(ctx, p) })
}

func sharedMemoryDBConn(t *testing.T, cfg oceanbase.Config, serve func(context.Context, net.Conn, *wire.PacketIO)) *sql.DB {
	t.Helper()
	p := testpeer.NewSharedMemoryPeer(t)
	cfg.Network, cfg.Addr, cfg.User = "sharedmemory", p.Name, publictest.TestUser
	created, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := &testpeer.SharedMemoryServer{Peer: p}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = server.Close() }()
		<-p.Ready
		serve(context.Background(), server, wire.NewPacketIO(server, 64<<20))
	}()
	t.Cleanup(func() { _ = server.Close(); <-done })
	db := sql.OpenDB(created)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPlatformTransportSharedMemoryBatch(t *testing.T) {
	for _, status := range []byte{2, 6} {
		for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchAuto, oceanbase.BatchMultiSend} {
			t.Run(fmt.Sprint(status, "/", strategy), func(t *testing.T) {
				db := sharedMemoryDB(t, oceanbase.Config{}, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.ServeConnect(ctx, t, p, status) || !testpeer.ServeBaseline(ctx, t, p, status) {
						return
					}
					for i := int64(1); i <= 3; i++ {
						if !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, i)) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, status)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
							return
						}
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				r, err := oceanbase.ExecBatch(t.Context(), db, testBatchSQL, [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: true})
				if err != nil || r.Strategy != oceanbase.BatchSequential || len(r.Items) != 3 {
					t.Fatal(r, err)
				}
				for _, item := range r.Items {
					if item.Status != oceanbase.BatchSucceeded || item.RowsAffected != 1 {
						t.Fatal(item)
					}
				}
			})
		}
	}
}
