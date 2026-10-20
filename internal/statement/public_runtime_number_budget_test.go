package statement_test

import (
	context "context"
	errors "errors"
	fmt "fmt"
	strings "strings"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestNumberSQLNormalizedBudget(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, variant := range []string{testStandard, testCompression, testOB20} {
			for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				t.Run(fmt.Sprintf("%d/%s/%d", mode, variant, prepare), func(t *testing.T) {
					status := byte(2)
					if mode == oceanbase.ModeOracle {
						status = 6
					}
					const query = "UPDATE t SET v=?"
					db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: mode, PrepareMode: prepare, Limits: oceanbase.Limits{MaxMessageBytes: 256}}, variant, func(ctx context.Context, p *wire.PacketIO) {
						payload := []byte("\x03UPDATE t SET v= 1 ")
						if prepare == oceanbase.PrepareServer {
							if !publictest.ServePrepareStatus(ctx, t, p, query, 87, 1, nil, status) {
								return
							}
							payload = append(publictest.ExecuteHeader(87), 0, 1, 246, 0, 1, '1')
						}
						if !testpeer.ServeOK(ctx, t, p, payload, status) {
							return
						}
						if prepare == oceanbase.PrepareServer && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 87)) {
							return
						}
						if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					s, err := db.PrepareContext(context.Background(), query)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = s.Close() }()
					if _, err = s.ExecContext(context.Background(), oceanbase.Number("1e"+strings.Repeat("0", 400))); err != nil {
						t.Fatal("fitting normalized number rejected", err)
					}
					if _, err = s.ExecContext(context.Background(), oceanbase.Number("1e256")); !errors.Is(err, wire.ErrMessageTooLarge) {
						t.Fatal("oversize decimal expansion accepted", err)
					}
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
					if err = db.PingContext(context.Background()); err != nil {
						t.Fatal("number rejection lost original session", err)
					}
				})
			}
		}
	}
}
