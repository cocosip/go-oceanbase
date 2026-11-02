package valuecodec_test

import (
	"context"
	"database/sql"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testURowIDCombined = "combined"

func TestURowIDSQLPaths(t *testing.T) {
	const text = "*AAEPAQAAwAEAAAA="
	const query = "SELECT ? FROM dual"
	for _, path := range []string{testServerStrategy, testClientStrategy, testURowIDCombined} {
		t.Run(path, func(t *testing.T) {
			db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) { serveURowIDSQLPaths(ctx, t, p, path) }, func(cfg *config.Config) {
				cfg.Execution.ReuseParameterTypes = true
				cfg.Execution.PrepareExecute = path == testURowIDCombined
				if path == testClientStrategy {
					cfg.PrepareMode = config.PrepareClient
				}
			})
			defer func() { _ = db.Close() }()
			var stmt *sql.Stmt
			if path == testServerStrategy {
				var err error
				stmt, err = db.PrepareContext(context.Background(), query)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stmt.Close() }()
			}
			for _, arg := range []sql.Null[oceanbase.URowID]{{}, {V: oceanbase.URowID(text), Valid: true}, {}} {
				var got sql.Null[oceanbase.URowID]
				var err error
				if stmt != nil {
					err = stmt.QueryRowContext(context.Background(), arg).Scan(&got)
				} else {
					err = db.QueryRowContext(context.Background(), query, arg).Scan(&got)
				}
				if err != nil || got != arg {
					t.Fatalf("got %+v want %+v: %v", got, arg, err)
				}
			}
		})
	}
}

func serveURowIDSQLPaths(ctx context.Context, t *testing.T, p *wire.PacketIO, path string) {
	const text = "*AAEPAQAAwAEAAAA="
	const query = "SELECT ? FROM dual"

	if path == testServerStrategy && !publictest.ServePrepareStatus(ctx, t, p, query, 41, 1, nil, 6) {
		return
	}
	for i, null := range []bool{true, false, true} {
		param := wire.Parameter{Type: 209, LengthEncoded: true, Null: null}
		if !null {
			param.Data = []byte(text)
		}
		switch path {
		case testClientStrategy:
			literal := "CAST('" + text + "' AS UROWID)"
			if null {
				literal = "CAST(NULL AS UROWID)"
			}
			if !testpeer.ReadQuery(ctx, t, p, "SELECT  "+literal+"  FROM dual") {
				return
			}
		case testServerStrategy:
			payload, err := wire.EncodeStmtExecuteOptions(41, []wire.Parameter{param}, 4096, wire.ExecuteOptions{ReuseTypes: i > 0})
			if err != nil || !testpeer.ReadCommand(ctx, t, p, payload) {
				return
			}
		case testURowIDCombined:
			payload, err := wire.EncodePrepareExecute(0, []byte(query), []wire.Parameter{param}, 4096)
			if err != nil || !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(41, 1, 1, 0), testpeer.TextColumn("p", 209, 63, 0), []byte{254, 0, 0, 6, 0}) {
				return
			}
		}
		if path != testURowIDCombined && !testpeer.SendPackets(ctx, t, p, []byte{1}) {
			return
		}
		row := append([]byte{byte(len(text))}, text...)
		if null {
			row = []byte{251}
		}
		if path != testClientStrategy {
			if null {
				row = []byte{0, 4}
			} else {
				row = append([]byte{0, 0}, row...)
			}
		}
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("v", 209, 63, 0), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) {
			return
		}
		if path == testURowIDCombined {
			if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 6, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 41)) {
				return
			}
		}
	}
	if path == testServerStrategy && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 41)) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}
