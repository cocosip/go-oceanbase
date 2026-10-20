//go:build integration

package call_test

import (
	sql "database/sql"
	fmt "fmt"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestReturning(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			if p.Mode != oceanbase.ModeOracle {
				t.Skip("RETURNING INTO is an Oracle compatibility mode capability")
			}
			db := integrationtest.OpenProfile(t, p)
			c, err := db.Conn(integrationtest.TestContext(t))
			if err != nil {
				integrationtest.FailOperation(t, "pin returning session", err)
			}
			defer func() { _ = c.Close() }()
			table := fmt.Sprintf("OBGR_%d", time.Now().UnixNano())
			if _, err = c.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+"(id NUMBER PRIMARY KEY,v VARCHAR2(40))"); err != nil {
				integrationtest.FailOperation(t, "create returning table", err)
			}
			defer func() {
				if _, err := c.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
					integrationtest.FailOperation(t, "drop returning table", err)
				}
			}()
			var id int64
			var value string
			result, err := c.ExecContext(integrationtest.TestContext(t), "INSERT INTO "+table+" VALUES(:x,'ok') RETURNING id,v INTO :id,:v", sql.Named("x", 42), sql.Named("id", sql.Out{Dest: &id}), sql.Named("v", sql.Out{Dest: &value}))
			if err != nil {
				integrationtest.FailOperation(t, "returning outputs", err)
			}
			count, err := result.RowsAffected()
			if err != nil || count != 1 || id != 42 || value != "ok" {
				t.Fatal("returning output or count mismatch")
			}
		})
	}
}

func TestScalarCall(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				t.Run(fmt.Sprint(prepare), func(t *testing.T) { integrationTestScalarCallProfile(t, p, prepare) })
			}
		})
	}
}
