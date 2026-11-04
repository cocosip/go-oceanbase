//go:build integration

package valuecodec_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

const sdoOwner = "SYS"

func TestOracleSDOGeometry(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeOracle {
			continue
		}
		t.Run(profile.Name, func(t *testing.T) {
			db := integrationtest.OpenProfile(t, profile)
			ctx := integrationtest.TestContext(t)
			table := fmt.Sprintf("OBGSD_%d", time.Now().UnixNano())
			if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,G SDO_GEOMETRY)"); err != nil {
				integrationtest.FailOperation(t, "create SDO table", err)
			}
			defer func() {
				if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
					integrationtest.FailOperation(t, "drop SDO table", err)
				}
			}()
			value := oceanbase.Object{Schema: sdoOwner, Type: "SDO_GEOMETRY", Fields: []oceanbase.Attribute{
				{Name: "SDO_GTYPE", Value: oceanbase.Number("2003")}, {Name: "SDO_SRID"}, {Name: "SDO_POINT"},
				{Name: "SDO_ELEM_INFO", Value: oceanbase.Array{Schema: sdoOwner, Type: "SDO_ELEM_INFO_ARRAY", Elements: []any{1, 1003, 1}}},
				{Name: "SDO_ORDINATES", Value: oceanbase.Array{Schema: sdoOwner, Type: "SDO_ORDINATE_ARRAY", Elements: []any{0, 0, 10, 0, 10, 10, 0, 10}}},
			}}
			if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,?)", value); err != nil {
				integrationtest.FailOperation(t, "bind generic SDO", err)
			}
			stmt, err := db.PrepareContext(ctx, "SELECT G FROM "+table+" WHERE ID=?")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stmt.Close() }()
			var got oceanbase.Object
			if err := stmt.QueryRowContext(ctx, 1).Scan(&got); err != nil {
				t.Fatalf("read generic SDO: %v", err)
			}
			if got.Schema != sdoOwner || got.Type != "SDO_GEOMETRY" || len(got.Fields) != 5 || got.Fields[0].Value != oceanbase.Number("2003") || got.Fields[1].Value != nil || got.Fields[2].Value != nil {
				t.Fatal("SDO object identity/scalars changed")
			}
			for i, want := range [][]string{{"1", "1003", "1"}, {"0", "0", "10", "0", "10", "10", "0", "10"}} {
				array, ok := got.Fields[i+3].Value.(oceanbase.Array)
				if !ok || len(array.Elements) != len(want) {
					t.Fatal("SDO collection shape changed")
				}
				for j, n := range want {
					if array.Elements[j] != oceanbase.Number(n) {
						t.Fatal("SDO ordinate changed")
					}
				}
			}
		})
	}
}
