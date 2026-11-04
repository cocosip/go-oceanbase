//go:build integration

package valuecodec_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// These server-side PL checks do not claim native ANYDATA or RECORD parameter support.
func TestOraclePLTypeReadiness(t *testing.T) {
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
			if _, err := db.ExecContext(ctx, "DECLARE V SYS.ANYDATA; N NUMBER; R PLS_INTEGER; BEGIN V:=SYS.ANYDATA.ConvertNumber(42); R:=V.GetNumber(N); IF R!=0 OR N IS NULL OR N!=42 THEN RAISE_APPLICATION_ERROR(-20001,'ANYDATA mismatch'); END IF; END;"); err != nil {
				integrationtest.FailOperation(t, "ANYDATA PL number round trip", err)
			}
			name := fmt.Sprintf("OBGPR_%d", time.Now().UnixNano())
			if _, err := db.ExecContext(ctx, "CREATE PACKAGE "+name+" AS TYPE R IS RECORD (N NUMBER,S VARCHAR2(40)); PROCEDURE P(V IN OUT R); END;"); err != nil {
				integrationtest.FailOperation(t, "create record package", err)
			}
			defer func() {
				if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP PACKAGE "+name); err != nil {
					integrationtest.FailOperation(t, "drop record package", err)
				}
			}()
			if _, err := db.ExecContext(ctx, "CREATE PACKAGE BODY "+name+" AS PROCEDURE P(V IN OUT R) AS BEGIN V.N:=V.N+1; V.S:=V.S||'!'; END; END;"); err != nil {
				integrationtest.FailOperation(t, "create record body", err)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM USER_OBJECTS WHERE OBJECT_NAME=? AND STATUS='VALID'", name).Scan(&count); err != nil || count != 2 {
				t.Fatal("record package invalid", count, err)
			}
			if _, err := db.ExecContext(ctx, "DECLARE V "+name+".R; BEGIN V.N:=41; V.S:='record'; "+name+".P(V); IF V.N IS NULL OR V.N!=42 OR V.S IS NULL OR V.S!='record!' THEN RAISE_APPLICATION_ERROR(-20001,'record mismatch'); END IF; END;"); err != nil {
				integrationtest.FailOperation(t, "record server-side PL round trip", err)
			}
			for _, tc := range []struct{ name, query string }{
				{"record-native-output", "DECLARE V " + name + ".R; BEGIN V.N:=41; V.S:='record'; ? := V; END;"},
				{"anydata-native-output", "BEGIN ? := SYS.ANYDATA.ConvertNumber(42); END;"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					conn, err := db.Conn(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = conn.Close() }()
					var output oceanbase.Object
					_, err = conn.ExecContext(ctx, tc.query, sql.Out{Dest: &output})
					var server *oceanbase.Error
					// The tested target rejects these outputs before a typed result reaches the client.
					if !errors.As(err, &server) || !integrationtest.OnlyServerErrorCode(err, 600) || server.SQLState != "0A000" {
						integrationtest.FailOperation(t, "expected native PL output rejection", err)
					}
					if err := conn.PingContext(ctx); err != nil {
						integrationtest.FailOperation(t, "reuse after native PL output rejection", err)
					}
				})
			}
			rows, err := db.QueryContext(ctx, "SELECT ATTR_NAME,ATTR_TYPE_NAME FROM ALL_PLSQL_TYPE_ATTRS WHERE PACKAGE_NAME=? ORDER BY ATTR_NO", name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			for _, want := range [][2]string{{"N", "NUMBER"}, {"S", "VARCHAR2"}} {
				var field, typ string
				if !rows.Next() {
					t.Fatal("missing record field", rows.Err())
				}
				if err := rows.Scan(&field, &typ); err != nil || field != want[0] || typ != want[1] {
					t.Fatal("record field mismatch", field, typ, err)
				}
			}
			if rows.Next() || rows.Err() != nil {
				t.Fatal("unexpected record fields", rows.Err())
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
