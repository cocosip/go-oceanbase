//go:build integration

package integration

import (
	"database/sql"
	"os"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

func TestMySQLCharsets(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.name, func(t *testing.T) {
			for _, tc := range []struct{ charset, text string }{
				{"utf8mb4", "中文🙂"}, {"utf8", "中文"}, {"latin1", "café €"},
				{"gbk", "中文"}, {"gb18030", "中文🙂"}, {"ascii", "ascii;\\'"},
			} {
				t.Run(tc.charset, func(t *testing.T) { testMySQLCharset(t, p, tc.charset, tc.text) })
			}
		})
	}
}

func testMySQLCharset(t *testing.T, p profile, charset, text string) {
	t.Helper()
	for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
		cfg := profileConfig(t, p)
		cfg.Session.Charset, cfg.PrepareMode = charset, prepare
		connector, err := oceanbase.NewConnector(cfg)
		if err != nil {
			failOperation(t, "charset connector", err)
		}
		db := sql.OpenDB(connector)
		func() {
			defer func() { _ = db.Close() }()
			ctx := testContext(t)
			c, err := db.Conn(ctx)
			if err != nil {
				failOperation(t, "charset connection", err)
			}
			defer func() { _ = c.Close() }()
			for _, mode := range []string{"", "NO_BACKSLASH_ESCAPES"} {
				if _, err = c.ExecContext(ctx, "SET SESSION sql_mode='"+mode+"'"); err != nil {
					failOperation(t, "charset SQL mode", err)
				}
				var got string
				if err = c.QueryRowContext(ctx, "SELECT ?", text).Scan(&got); err != nil {
					failOperation(t, "charset round trip", err)
				}
				if got != text {
					t.Fatalf("charset=%s prepare=%d mode=%s: got=%q want=%q", charset, prepare, mode, got, text)
				}
			}
		}()
	}
}
