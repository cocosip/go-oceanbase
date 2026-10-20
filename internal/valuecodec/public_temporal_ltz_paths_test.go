package valuecodec_test

import (
	"context"
	"errors"
	"fmt"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"testing"
	"time"
)

const ltzSQLPrefix = "CAST(TIMESTAMP '"
const ltzSQLSuffix = "' AS TIMESTAMP(9) WITH LOCAL TIME ZONE)"
const ltzSummerNewYork = "2024-07-01 12:34:56.123456789 America/New_York"
const ltzSpringGap = "2024-03-10 02:30:00 America/New_York"

func TestLTZSQLPrepareEquivalence(t *testing.T) {
	for _, location := range []*time.Location{time.UTC, time.FixedZone("America/New_York", 0)} {
		for _, variant := range []string{publictest.TestStandard, publictest.TestCompression, publictest.TestOB20} {
			for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				t.Run(fmt.Sprintf("%s/%s/%d", location, variant, prepare), func(t *testing.T) {
					const query = "UPDATE t SET v=?"
					db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: oceanbase.ModeOracle, PrepareMode: prepare, Session: oceanbase.SessionOptions{Location: location}}, variant, func(ctx context.Context, p *wire.PacketIO) {
						payload := []byte("\x03UPDATE t SET v= " + ltzSQLPrefix + "2024-07-01 16:34:56.123456789" + ltzSQLSuffix + " ")
						if prepare == oceanbase.PrepareServer {
							if !publictest.ServePrepareStatus(ctx, t, p, query, 88, 1, nil, 6) {
								return
							}
							payload = append(publictest.ExecuteHeader(88), 0, 1, 201, 0, 12, 20, 24, 7, 1, 16, 34, 56, 21, 205, 91, 7, 9)
						}
						if !testpeer.ServeOK(ctx, t, p, payload, 6) {
							return
						}
						if prepare == oceanbase.PrepareServer && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 88)) {
							return
						}
						if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
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
					if _, err = s.ExecContext(context.Background(), oceanbase.TimestampLTZ(ltzSummerNewYork)); err != nil {
						t.Fatal("resolvable region rejected", err)
					}
					if _, err = s.ExecContext(context.Background(), oceanbase.TimestampLTZ(ltzSpringGap)); !errors.Is(err, oceanbase.ErrType) {
						t.Fatal("source gap accepted", err)
					}
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
					if err = db.PingContext(context.Background()); err != nil {
						t.Fatal("rejection disturbed physical session", err)
					}
				})
			}
		}
	}
}
