//go:build integration

package statement_test

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestMySQLMultiStatementLongData(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(profile.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, profile)
			cfg.PrepareMode, cfg.AllowMultiStatements = oceanbase.PrepareClient, true
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			want := strings.Repeat("quoted'\\\x00中文", 1024)
			stream := &oceanbase.LongData{Reader: strings.NewReader(want), Length: int64(len(want)), MaxBytes: int64(len(want)), Text: true}
			rows, err := db.QueryContext(integrationtest.TestContext(t), "SELECT ?; SELECT ?", stream, 42)
			if err != nil {
				integrationtest.FailOperation(t, "multi-statement LongData", err)
			}
			defer func() { _ = rows.Close() }()
			var got string
			if !rows.Next() {
				t.Fatal("missing stream result", rows.Err())
			}
			if err := rows.Scan(&got); err != nil || got != want {
				t.Fatal("stream content mismatch", err)
			}
			if rows.Next() || !rows.NextResultSet() {
				t.Fatal("missing next result", rows.Err())
			}
			var number int
			if !rows.Next() {
				t.Fatal("missing scalar result", rows.Err())
			}
			if err := rows.Scan(&number); err != nil || number != 42 {
				t.Fatal(number, err)
			}
			if rows.Next() || rows.NextResultSet() || rows.Err() != nil {
				t.Fatal("unexpected result", rows.Err())
			}
		})
	}
}
