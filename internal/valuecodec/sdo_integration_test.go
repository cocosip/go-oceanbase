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

const sdoOwner = "SYS"
const sdoGeometryType = "SDO_GEOMETRY"

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
			observer := integrationtest.OpenProfile(t, profile)
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
			value := oceanbase.Object{Schema: sdoOwner, Type: sdoGeometryType, Fields: []oceanbase.Attribute{
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
			testSDOPoint(t, db, observer, table)
		})
	}
}

func testSDOPoint(t *testing.T, db, observer *sql.DB, table string) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	point := oceanbase.Object{Schema: sdoOwner, Type: "SDO_POINT_TYPE", Fields: []oceanbase.Attribute{
		{Name: "X", Value: oceanbase.Number("116")}, {Name: "Y", Value: oceanbase.Number("39")}, {Name: "Z"},
	}}
	value := oceanbase.Object{Schema: sdoOwner, Type: sdoGeometryType, Fields: []oceanbase.Attribute{
		{Name: "SDO_GTYPE", Value: oceanbase.Number("2001")}, {Name: "SDO_SRID", Value: oceanbase.Number("0")},
		{Name: "SDO_POINT", Value: point}, {Name: "SDO_ELEM_INFO"}, {Name: "SDO_ORDINATES"},
	}}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES(2,?)", value); err != nil {
		integrationtest.FailOperation(t, "store SDO point and SRID", err)
	}
	stmt, err := observer.PrepareContext(ctx, "SELECT G FROM "+table+" WHERE ID=?")
	if err != nil {
		integrationtest.FailOperation(t, "prepare SDO point", err)
	}
	defer func() { _ = stmt.Close() }()
	var got oceanbase.Object
	if err := stmt.QueryRowContext(ctx, 2).Scan(&got); err != nil {
		integrationtest.FailOperation(t, "read SDO point", err)
	}
	if len(got.Fields) != 5 || got.Fields[0].Value != oceanbase.Number("2001") || got.Fields[1].Value != oceanbase.Number("0") {
		t.Fatal("SDO point GTYPE/SRID changed", got)
	}
	nested, ok := got.Fields[2].Value.(oceanbase.Object)
	if !ok || nested.Schema != sdoOwner || nested.Type != point.Type || len(nested.Fields) != 3 || nested.Fields[0].Value != oceanbase.Number("116") || nested.Fields[1].Value != oceanbase.Number("39") || nested.Fields[2].Value != nil {
		t.Fatal("SDO point members changed", got)
	}
	var srid, x, y int
	if err := observer.QueryRowContext(ctx, "SELECT t.G.SDO_SRID,t.G.SDO_POINT.X,t.G.SDO_POINT.Y FROM "+table+" t WHERE ID=2").Scan(&srid, &x, &y); err != nil || srid != 0 || x != 116 || y != 39 {
		t.Fatal("SDO point scalar storage changed", srid, x, y, err)
	}
	value.Fields[1].Value = oceanbase.Number("4326")
	_, err = db.ExecContext(ctx, "INSERT INTO "+table+" VALUES(3,?)", value)
	if !integrationtest.OnlyServerErrorCode(err, 13029) {
		integrationtest.FailOperation(t, "expected native invalid SRID rejection", err)
	}
	_, err = db.ExecContext(ctx, "INSERT INTO "+table+" VALUES(3,SDO_GEOMETRY(2001,4326,SDO_POINT_TYPE(116,39,NULL),NULL,NULL))")
	if !integrationtest.OnlyServerErrorCode(err, 13029) {
		integrationtest.FailOperation(t, "expected SQL invalid SRID rejection", err)
	}
	var count int
	if err := observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ID=3").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid SRID left a stored row", count, err)
	}
}
