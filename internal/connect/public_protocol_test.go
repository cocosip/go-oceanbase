package connect_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestPacketModes(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, variant := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprint(mode, "/", variant), func(t *testing.T) {
				status := byte(2)
				if mode == config.ModeOracle {
					status = 6
				}
				options := config.ProtocolOptions{OB20: config.FeatureOff, Compression: config.FeatureOff}
				if variant == testCompression {
					options.Compression = config.FeatureRequired
				}
				if variant == testOB20 {
					options.OB20 = config.FeatureRequired
				}
				dial := testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolGreeting(variant == testCompression)) {
						return
					}
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					vars := map[string]string{}
					if variant == testOB20 {
						vars[testProxyCapability] = "33024"
						vars["__ob_client_capability_flag"] = "2"
					}
					if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(status, vars)) {
						return
					}
					if variant == testCompression {
						if err := p.EnableCompression(); err != nil {
							t.Error(err)
							return
						}
					}
					if variant == testOB20 {
						if err := p.EnableOB20(wire.OB20Options{ConnectionID: 42, Checksum: true, NewExtra: true}); err != nil {
							t.Error(err)
							return
						}
					}
					if !testpeer.ServeBaseline(ctx, t, p, status) {
						return
					}
					ctx = context.Background()
					if !testpeer.ReadQuery(ctx, t, p, "SELECT v") {
						return
					}
					if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 253, 45, 0), []byte{254, 0, 0, status, 0}, []byte{3, 'a', 'b', 'c'}, []byte{254, 0, 0, status, 0}) {
						return
					}
					for i := int64(1); i <= 2; i++ {
						if !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) || !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, i)) || !testpeer.SendPackets(ctx, t, p, commandPacket(1, 0, status)) || !testpeer.ReadCommand(ctx, t, p, stmtPacket(25, 7)) {
							return
						}
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				connector, err := oceanbase.NewConnector(config.Config{User: testUser, Mode: mode, Protocol: options, DialContext: dial})
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				defer func() { _ = db.Close() }()
				var got string
				if err = db.QueryRowContext(context.Background(), "SELECT v").Scan(&got); err != nil || got != "abc" {
					t.Fatal(got, err)
				}
				batch, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchMultiSend, ContinueOnError: variant != testStandard})
				if err != nil || batch.Strategy != oceanbase.BatchSequential || len(batch.Items) != 2 || batch.Items[1].Status != oceanbase.BatchSucceeded {
					t.Fatal(batch, err)
				}
			})
		}
	}
}
