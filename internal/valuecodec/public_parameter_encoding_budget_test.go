package valuecodec_test

import (
	"context"
	"errors"
	"fmt"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"strings"
	"testing"
)

func TestPreparedParameterEncodedBudget(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, national := range []bool{false, true} {
			if national && mode != oceanbase.ModeOracle {
				continue
			}
			for _, variant := range []string{publictest.TestStandard, publictest.TestCompression, publictest.TestOB20} {
				t.Run(fmt.Sprintf("%d/%t/%s", mode, national, variant), func(t *testing.T) {
					status, typ := byte(2), uint16(253)
					if mode == oceanbase.ModeOracle {
						status = 6
					}
					if national {
						typ = 208
					}
					encoded := []byte(strings.Repeat("\xb2\xe2", 150))
					payload, err := wire.EncodeStmtExecute(84, []wire.Parameter{{Type: typ, Data: encoded, LengthEncoded: true}}, 4096)
					if err != nil {
						t.Fatal(err)
					}
					db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: mode, Session: oceanbase.SessionOptions{Charset: localFilenameCharset}, Limits: oceanbase.Limits{MaxMessageBytes: int64(len(payload))}}, variant, func(ctx context.Context, p *wire.PacketIO) {
						if !publictest.ServePrepareStatus(ctx, t, p, testBatchSQL, 84, 1, nil, status) || !testpeer.ServeOK(ctx, t, p, payload, status) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 84)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					s, err := db.PrepareContext(context.Background(), testBatchSQL)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = s.Close() }()
					text := strings.Repeat("测", 150)
					value, oversized := any(text), any(text+"x")
					if national {
						value, oversized = oceanbase.NString(text), oceanbase.NString(text+"x")
					}
					if _, err = s.ExecContext(context.Background(), value); err != nil {
						t.Fatal("fitting encoded parameter was rejected", err)
					}
					if _, err = s.ExecContext(context.Background(), oversized); !errors.Is(err, wire.ErrMessageTooLarge) {
						t.Fatal("length prefix or execution header exceeded message budget", err)
					}
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
					if err = db.PingContext(context.Background()); err != nil {
						t.Fatal("pre-send rejection damaged the physical session", err)
					}
				})
			}
		}
	}
}
