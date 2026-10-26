//go:build integration

package valuecodec_test

import (
	"database/sql"
	"os"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestParsedTemporalTypes(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		t.Run(profile.Name, func(t *testing.T) {
			for _, offset := range []int{0, 8 * 3600} {
				for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
					t.Run((time.Duration(offset)*time.Second).String()+"/"+path, func(t *testing.T) {
						cfg := integrationtest.ProfileConfig(t, profile)
						loc := time.FixedZone("", offset)
						cfg.Session.ParseTime, cfg.Session.Location = true, loc
						cfg.PrepareMode = oceanbase.PrepareServer
						if path == testClientStrategy {
							cfg.PrepareMode = oceanbase.PrepareClient
						}
						connector, err := oceanbase.NewConnector(cfg)
						if err != nil {
							t.Fatal(err)
						}
						db := sql.OpenDB(connector)
						defer func() { _ = db.Close() }()
						query := "SELECT CAST('2024-02-29' AS DATE),CAST('2024-06-01 12:30:45.123456' AS DATETIME(6))"
						want := []time.Time{time.Date(2024, 2, 29, 0, 0, 0, 0, loc), time.Date(2024, 6, 1, 12, 30, 45, 123456000, loc)}
						if profile.Mode == oceanbase.ModeOracle {
							query = "SELECT CAST(TIMESTAMP '2024-02-29 12:34:56' AS DATE),TIMESTAMP '2024-06-01 12:30:45.123456789',TIMESTAMP '2024-06-01 12:30:45.123456789 -00:30',CAST(TIMESTAMP '2024-06-01 12:30:45.123456789 +00:00' AS TIMESTAMP(9) WITH LOCAL TIME ZONE) FROM dual"
							want = []time.Time{time.Date(2024, 2, 29, 12, 34, 56, 0, loc), time.Date(2024, 6, 1, 12, 30, 45, 123456789, loc), time.Date(2024, 6, 1, 12, 30, 45, 123456789, time.FixedZone("", -1800)), time.Date(2024, 6, 1, 12, 30, 45, 123456789, time.UTC).In(loc)}
						}
						ctx := integrationtest.TestContext(t)
						var rows *sql.Rows
						if path == testTextStrategy {
							rows, err = db.QueryContext(ctx, query)
						} else {
							stmt, e := db.PrepareContext(ctx, query)
							if e != nil {
								integrationtest.FailOperation(t, "prepare parsed temporal query", e)
							}
							defer func() { _ = stmt.Close() }()
							rows, err = stmt.QueryContext(ctx)
						}
						if err != nil {
							integrationtest.FailOperation(t, "query parsed temporal values", err)
						}
						defer func() { _ = rows.Close() }()
						if !rows.Next() {
							t.Fatal("temporal row missing", rows.Err())
						}
						got, dest := make([]time.Time, len(want)), make([]any, len(want))
						for i := range got {
							dest[i] = &got[i]
						}
						if err := rows.Scan(dest...); err != nil {
							integrationtest.FailOperation(t, "scan parsed temporal values", err)
						}
						for i, instant := range got {
							if !instant.Equal(want[i]) || instant.Format("-07:00") != want[i].Format("-07:00") {
								t.Fatalf("column %d got %v, want %v", i, instant, want[i])
							}
						}
						if rows.Next() || rows.Err() != nil {
							t.Fatal("temporal result boundary changed", rows.Err())
						}
					})
				}
			}
		})
	}
}
