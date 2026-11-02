//go:build integration

package valuecodec_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestOracleURowID(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeOracle {
			continue
		}
		for _, path := range []string{testServerStrategy, testClientStrategy, testURowIDCombined} {
			t.Run(profile.Name+"/"+path, func(t *testing.T) {
				cfg := integrationtest.ProfileConfig(t, profile)
				cfg.Execution.PrepareExecute = path == "combined"
				cfg.Execution.ReuseParameterTypes = true
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
				table := fmt.Sprintf("OBGU_%d", time.Now().UnixNano())
				if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID VARCHAR2(1000) PRIMARY KEY,U UROWID(4000))"); err != nil {
					integrationtest.FailOperation(t, "create UROWID table", err)
				}
				defer func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, err := db.ExecContext(cleanup, "DROP TABLE "+table); err != nil {
						integrationtest.FailOperation(t, "drop UROWID table", err)
					}
				}()
				if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,NULL)", strings.Repeat("z", 900)); err != nil {
					integrationtest.FailOperation(t, "insert UROWID source", err)
				}
				var id oceanbase.URowID
				if err := db.QueryRowContext(ctx, "SELECT ROWID FROM "+table).Scan(&id); err != nil {
					integrationtest.FailOperation(t, "read UROWID source", err)
				}
				if len(id) <= 251 {
					t.Fatal("source did not exercise a long UROWID")
				}
				stmt, err := db.PrepareContext(ctx, "INSERT INTO "+table+" VALUES (?,?)")
				if err != nil {
					integrationtest.FailOperation(t, "prepare UROWID insert", err)
				}
				defer func() { _ = stmt.Close() }()
				for i, want := range []sql.Null[oceanbase.URowID]{{}, {V: id, Valid: true}, {}} {
					key := fmt.Sprint(i)
					if _, err := stmt.ExecContext(ctx, key, want); err != nil {
						integrationtest.FailOperation(t, "bind UROWID", err)
					}
					var got sql.Null[oceanbase.URowID]
					if err := db.QueryRowContext(ctx, "SELECT U FROM "+table+" WHERE ID=?", key).Scan(&got); err != nil {
						integrationtest.FailOperation(t, "read UROWID", err)
					}
					if got != want {
						t.Fatal("UROWID or NULL changed")
					}
					got = want
					if _, err := db.ExecContext(ctx, "BEGIN ? := ?; END;", oceanbase.Out{Dest: &got, In: true}, want); err != nil {
						integrationtest.FailOperation(t, "UROWID INOUT", err)
					}
					if got != want {
						t.Fatal("INOUT UROWID changed")
					}
				}
			})
		}
	}
}
