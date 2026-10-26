package valuecodec_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestMySQLTimeSQLPrepare(t *testing.T) {
	for _, variant := range []string{publictest.TestStandard, publictest.TestCompression, publictest.TestOB20} {
		for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
			t.Run(fmt.Sprintf("%s/%d", variant, prepare), func(t *testing.T) {
				const query = "UPDATE t SET v=?"
				db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: oceanbase.ModeMySQL, PrepareMode: prepare}, variant, func(ctx context.Context, p *wire.PacketIO) {
					payload := []byte("\x03UPDATE t SET v= '2026-01-02 03:04:05.123456' ")
					if prepare == oceanbase.PrepareServer {
						if !publictest.ServePrepareStatus(ctx, t, p, query, 88, 1, nil, 2) {
							return
						}
						payload = append(publictest.ExecuteHeader(88), 0, 1, 12, 0, 11, 234, 7, 1, 2, 3, 4, 5, 64, 226, 1, 0)
					}
					for range 3 {
						if !testpeer.ServeOK(ctx, t, p, payload, 2) {
							return
						}
					}
					if prepare == oceanbase.PrepareServer && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 88)) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				stmt, err := db.PrepareContext(context.Background(), query)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stmt.Close() }()
				value := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
				for _, arg := range []any{value, &value, sql.NullTime{Time: value, Valid: true}} {
					if _, err := stmt.ExecContext(context.Background(), arg); err != nil {
						t.Fatalf("%T timestamp rejected: %v", arg, err)
					}
				}
			})
		}
	}
}
