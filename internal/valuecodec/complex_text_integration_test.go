//go:build integration

package valuecodec_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestOracleNamedObjectText(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeOracle {
			continue
		}
		t.Run(profile.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, profile)
			cfg.PrepareMode = oceanbase.PrepareClient
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			db.SetMaxOpenConns(1)
			defer func() { _ = db.Close() }()
			ctx := integrationtest.TestContext(t)
			base := fmt.Sprintf("OBGT_%d", time.Now().UnixNano())
			child, root, table := base+"C", base+"R", base+"T"
			var created []string
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				for i := len(created) - 1; i >= 0; i-- {
					if _, err := db.ExecContext(cleanup, "DROP "+created[i]); err != nil {
						t.Errorf("drop text OBJECT fixture: %v", err)
					}
				}
			}()
			for _, ddl := range []struct{ identity, body string }{
				{"TYPE " + child, " AS OBJECT (N NUMBER,S VARCHAR2(80),U NVARCHAR2(80))"},
				{"TYPE " + root, " AS OBJECT (C " + child + ",TAG NUMBER)"},
				{"TABLE " + table, " (ID INTEGER PRIMARY KEY,V " + root + ")"},
			} {
				if _, err := db.ExecContext(ctx, "CREATE "+ddl.identity+ddl.body); err != nil {
					integrationtest.FailOperation(t, "create text OBJECT fixture", err)
				}
				created = append(created, ddl.identity)
			}
			for _, query := range []string{
				"INSERT INTO " + table + " VALUES (1," + root + "(" + child + "(7,'a,b (c) ''q''',N'患者😀'),9))",
				"INSERT INTO " + table + " VALUES (2," + root + "(NULL,NULL))",
			} {
				if _, err := db.ExecContext(ctx, query); err != nil {
					integrationtest.FailOperation(t, "insert text OBJECT fixture", err)
				}
			}
			for _, client := range []bool{false, true} {
				query, args := "SELECT V FROM "+table+" WHERE ID=1", []any(nil)
				if client {
					query, args = "SELECT V FROM "+table+" WHERE ID=?", []any{1}
				}
				var value oceanbase.Object
				if err := db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
					integrationtest.FailOperation(t, "read nested text OBJECT", err)
				}
				if value.Type != root || len(value.Fields) != 2 || value.Fields[1].Value != oceanbase.Number("9") {
					t.Fatal("root text OBJECT changed")
				}
				nested, ok := value.Fields[0].Value.(oceanbase.Object)
				if !ok || nested.Type != child || len(nested.Fields) != 3 || nested.Fields[0].Value != oceanbase.Number("7") || nested.Fields[1].Value != "a,b (c) 'q'" || nested.Fields[2].Value != "患者😀" {
					t.Fatal("nested text OBJECT changed")
				}
				if err := db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=2").Scan(&value); err != nil {
					integrationtest.FailOperation(t, "read text OBJECT NULL members", err)
				}
				if value.Null || value.Fields[0].Value != nil || value.Fields[1].Value != nil {
					t.Fatal("all NULL attributes confused with root NULL")
				}
			}
		})
	}
}
