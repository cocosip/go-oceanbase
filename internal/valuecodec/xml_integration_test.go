//go:build integration

package valuecodec_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestOracleXMLType(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeOracle {
			continue
		}
		for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy, testURowIDCombined} {
			t.Run(profile.Name+"/"+path, func(t *testing.T) {
				cfg := integrationtest.ProfileConfig(t, profile)
				cfg.Execution.PrepareExecute = path == testURowIDCombined
				if path == testClientStrategy {
					cfg.PrepareMode = oceanbase.PrepareClient
				}
				connector, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				db.SetMaxOpenConns(1)
				defer func() { _ = db.Close() }()
				ctx := integrationtest.TestContext(t)
				table := fmt.Sprintf("OBGX_%d", time.Now().UnixNano())
				if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID NUMBER PRIMARY KEY,X XMLTYPE)"); err != nil {
					integrationtest.FailOperation(t, "create XML table", err)
				}
				defer func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, err := db.ExecContext(cleanup, "DROP TABLE "+table); err != nil {
						integrationtest.FailOperation(t, "drop XML table", err)
					}
				}()
				if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,XMLTYPE('<x>患者😀</x>'))"); err != nil {
					integrationtest.FailOperation(t, "insert XML", err)
				}
				if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (2,NULL)"); err != nil {
					integrationtest.FailOperation(t, "insert XML NULL", err)
				}
				function := table + "F"
				if _, err := db.ExecContext(ctx, "CREATE FUNCTION "+function+" RETURN XMLTYPE IS BEGIN RETURN XMLTYPE('<x>患者😀</x>'); END;"); err != nil {
					integrationtest.FailOperation(t, "create XML function", err)
				}
				defer func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, err := db.ExecContext(cleanup, "DROP FUNCTION "+function); err != nil {
						integrationtest.FailOperation(t, "drop XML function", err)
					}
				}()

				for shape, query := range []string{"SELECT X FROM " + table + " WHERE ID>=? ORDER BY ID", "SELECT " + function + "() FROM dual WHERE ?=1"} {
					var rows *sql.Rows
					var stmt *sql.Stmt
					switch path {
					case testTextStrategy:
						rows, err = db.QueryContext(ctx, strings.Replace(query, "?", "1", 1))
					case testClientStrategy:
						rows, err = db.QueryContext(ctx, query, 1)
					default:
						stmt, err = db.PrepareContext(ctx, query)
						if err == nil {
							rows, err = stmt.QueryContext(ctx, 1)
						}
					}
					if err != nil {
						integrationtest.FailOperation(t, "query XML", err)
					}
					verifyOracleXMLRows(t, rows, shape == 0)
					if err := rows.Close(); err != nil {
						integrationtest.FailOperation(t, "close XML rows", err)
					}
					if stmt != nil {
						if err := stmt.Close(); err != nil {
							integrationtest.FailOperation(t, "close XML statement", err)
						}
					}
				}
			})
		}
	}
}

func verifyOracleXMLRows(t *testing.T, rows *sql.Rows, wantNull bool) {
	t.Helper()
	columns, err := rows.ColumnTypes()
	if err != nil || columns[0].DatabaseTypeName() != "XMLTYPE" || columns[0].ScanType() != reflect.TypeFor[string]() {
		t.Fatal("XML metadata mismatch")
	}
	var got sql.Null[string]
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	if err := rows.Scan(&got); err != nil || !got.Valid || got.V != "<x>患者😀</x>\n" {
		t.Fatal("XML value mismatch", err)
	}
	if wantNull {
		if !rows.Next() {
			t.Fatal(rows.Err())
		}
		if err := rows.Scan(&got); err != nil || got.Valid {
			t.Fatal("XML NULL mismatch", err)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
}
