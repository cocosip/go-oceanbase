//go:build integration

package valuecodec_test

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// These are exact target semantics, including accepted normalization of NaN and duplicate sparse indices.
func TestMySQLExtendedBoundaries(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeMySQL {
			continue
		}
		for _, client := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/client=%t", profile.Name, client), func(t *testing.T) {
				cfg := integrationtest.ProfileConfig(t, profile)
				cfg.PrepareMode = oceanbase.PrepareServer
				if client {
					cfg.PrepareMode = oceanbase.PrepareClient
				}
				connector, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				t.Cleanup(func() { _ = db.Close() })
				observer := integrationtest.OpenProfile(t, profile)
				for _, group := range []struct {
					typ   string
					cases []extendedBoundary
				}{
					{"VECTOR(3)", []extendedBoundary{
						{"[-1,0,1]", "[-1,0,1]", 0},
						{"[1,2]", "", 7600}, {"[1,2,3,4]", "", 7600},
						{"[NaN,0,0]", "[0,0,0]", 0}, {"[1e400,0,0]", "", 1210},
						{"[-3.402823466e38,0,3.402823466e38]", "[-3.40282e38,0,3.40282e38]", 0},
					}},
					{"SPARSEVECTOR", []extendedBoundary{
						{"{}", "{}", 0}, {"{2:1,4:2}", "{2:1,4:2}", 0},
						{"{1:1,1:2}", "{1:2}", 0}, {"{-1:1}", "", 1264},
						{"{1:NaN}", "", 3140}, {"{1:1e400}", "", 3140},
					}},
					{"ARRAY(INT)", []extendedBoundary{
						{"[]", "[]", 0}, {"[2147483647,-2147483648]", "[2147483647,-2147483648]", 0},
						{"[NULL,1]", "[NULL,1]", 0}, {"[2147483648]", "", 1264},
						{"[-2147483649]", "", 1264}, {"[abc]", "", 3140},
					}},
					{"ARRAY(ARRAY(INT))", []extendedBoundary{
						{"[[1,2],[],NULL]", "[[1,2],[],NULL]", 0}, {"[]", "", 7602},
						{"[[2147483648]]", "", 1264},
					}},
				} {
					t.Run(group.typ, func(t *testing.T) {
						table := fmt.Sprintf("OBGXB_%d", time.Now().UnixNano())
						createTemporalNationalFixture(t, db, "TABLE", table, " (ID INTEGER PRIMARY KEY,V "+group.typ+")")
						for id, tc := range group.cases {
							t.Run(fmt.Sprint(id), func(t *testing.T) { checkExtendedBoundary(t, db, observer, table, id, tc) })
						}
					})
				}
			})
		}
	}
}

type extendedBoundary struct {
	input, want string
	code        uint32
}

func checkExtendedBoundary(t *testing.T, db, observer *sql.DB, table string, id int, tc extendedBoundary) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(ctx, "INSERT INTO "+table+" VALUES(?,?)", id, tc.input)
	if tc.code != 0 {
		if !integrationtest.OnlyServerErrorCode(err, tc.code) {
			integrationtest.FailOperation(t, fmt.Sprintf("expected extended rejection %d", tc.code), err)
		}
		var count int
		if err := observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ID=?", id).Scan(&count); err != nil || count != 0 {
			t.Fatal("rejected input left a stored row", count, err)
		}
	} else {
		if err != nil {
			integrationtest.FailOperation(t, "store boundary value", err)
		}
		for _, query := range []string{"SELECT V FROM " + table + " WHERE ID=" + fmt.Sprint(id), "SELECT V FROM " + table + " WHERE ID=?"} {
			var args []any
			if query[len(query)-1] == '?' {
				args = []any{id}
			}
			var got string
			if err := observer.QueryRowContext(ctx, query, args...).Scan(&got); err != nil || got != tc.want {
				t.Fatal("independently stored boundary changed", got, err)
			}
		}
	}
	if err := conn.PingContext(ctx); err != nil {
		integrationtest.FailOperation(t, "reuse after extended boundary", err)
	}
}
