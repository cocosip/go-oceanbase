//go:build integration

package valuecodec_test

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

const extendedArray = "[1,2,3]"

// TestMySQLExtendedTypes checks existing scalar bindings against real typed columns.
func TestMySQLExtendedTypes(t *testing.T) {
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
				if client {
					cfg.PrepareMode = oceanbase.PrepareClient
				}
				connector, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				defer func() { _ = db.Close() }()
				for _, tc := range []struct {
					name, typ, expression, selectSQL string
					value                            any
					want                             string
				}{
					{"json", "JSON", "?", "JSON_UNQUOTE(JSON_EXTRACT(V,'$.name'))", `{"name":"病人😀"}`, "病人😀"},
					{"bit", "BIT(8)", "?", "CAST(V AS UNSIGNED)", uint64(165), "165"},
					{"year", "YEAR", "?", "V", 2026, "2026"},
					{"geometry", "GEOMETRY", "ST_GeomFromText(?)", "ST_AsText(V)", "POINT(1 2)", "POINT(1 2)"},
					{"vector", "VECTOR(3)", "?", "V", extendedArray, extendedArray},
					{"array", "ARRAY(INT)", "?", "V", extendedArray, extendedArray},
					{"sparse", "SPARSEVECTOR", "?", "V", "{1:1,3:2}", "{1:1,3:2}"},
				} {
					t.Run(tc.name, func(t *testing.T) {
						ctx := integrationtest.TestContext(t)
						table := fmt.Sprintf("OBGE_%d", time.Now().UnixNano())
						if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V "+tc.typ+")"); err != nil {
							integrationtest.FailOperation(t, "create extended type", err)
						}
						defer func() {
							if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
								integrationtest.FailOperation(t, "drop extended type", err)
							}
						}()
						for _, row := range []struct {
							id    int
							value any
						}{{1, tc.value}, {2, nil}} {
							if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,"+tc.expression+")", row.id, row.value); err != nil {
								integrationtest.FailOperation(t, "insert extended type", err)
							}
						}
						for _, bound := range []bool{false, true} {
							query := "SELECT " + tc.selectSQL + " FROM " + table + " WHERE ID>=1 ORDER BY ID"
							var args []any
							if bound {
								query = "SELECT " + tc.selectSQL + " FROM " + table + " WHERE ID>=? ORDER BY ID"
								args = []any{1}
							}
							rows, err := db.QueryContext(ctx, query, args...)
							if err != nil {
								integrationtest.FailOperation(t, "read extended type", err)
							}
							columns, err := rows.ColumnTypes()
							if err != nil {
								t.Fatal(err)
							}
							checkExtendedMetadata(t, tc.name, columns[0])
							var got sql.Null[string]
							if !rows.Next() {
								t.Fatal("missing typed value", rows.Err())
							}
							if err := rows.Scan(&got); err != nil || !got.Valid || got.V != tc.want {
								t.Fatalf("extended value got=%q want=%q error=%v", got.V, tc.want, err)
							}
							if !rows.Next() {
								t.Fatal("missing typed NULL", rows.Err())
							}
							if err := rows.Scan(&got); err != nil || got.Valid {
								t.Fatal("typed NULL changed", err)
							}
							if rows.Next() || rows.Err() != nil {
								t.Fatal("unexpected typed rows", rows.Err())
							}
							if err := rows.Close(); err != nil {
								t.Fatal(err)
							}
						}
						if tc.name == "geometry" {
							checkGeometryRoundTrip(t, db, table, tc.want)
						}
					})
				}
			})
		}
	}
}

func checkGeometryRoundTrip(t *testing.T, db *sql.DB, table, wantText string) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	var raw []byte
	if err := db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("000000000101000000000000000000F03F0000000000000040")
	if !bytes.Equal(raw, want) {
		t.Fatal("geometry SRID/WKB changed")
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (3,?)", raw); err != nil {
		integrationtest.FailOperation(t, "bind geometry bytes", err)
	}
	var text string
	if err := db.QueryRowContext(ctx, "SELECT ST_AsText(V) FROM "+table+" WHERE ID=3").Scan(&text); err != nil || text != wantText {
		t.Fatal("geometry bytes round trip", err)
	}
}

func checkExtendedMetadata(t *testing.T, name string, column *sql.ColumnType) {
	t.Helper()
	if name == "vector" || name == "array" || name == "sparse" {
		if column.DatabaseTypeName() != "CHAR" || column.ScanType() != reflect.TypeFor[string]() {
			t.Fatal("extended text metadata changed", column.DatabaseTypeName())
		}
	}
}
