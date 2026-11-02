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

func TestOracleClientConstructors(t *testing.T) {
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
			cfg.PrepareMode, cfg.AllowMultiStatements = oceanbase.PrepareClient, true
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			verification := sql.OpenDB(connector)
			defer func() { _ = verification.Close() }()
			verifyClientConstructors(t, db, verification)
		})
	}
}

func verifyClientConstructors(t *testing.T, db, verification *sql.DB) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	base := fmt.Sprintf("OBGK_%d", time.Now().UnixNano())
	child, root, array := base+"C", base+"R", base+"A"
	table, elements, proc := base+"T", base+"E", base+"P"
	var created []string
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := db.ExecContext(cleanup, "DROP "+created[i]); err != nil {
				t.Errorf("drop client constructor fixture: %v", err)
			}
		}
	}()
	for _, ddl := range []struct{ identity, body string }{
		{"TYPE " + child, " AS OBJECT (N NUMBER,S VARCHAR2(80),U NVARCHAR2(80),R RAW(20),D DATE,TS TIMESTAMP(9))"},
		{"TYPE " + root, " AS OBJECT (C " + child + ",TAG NUMBER)"},
		{"TYPE " + array, " AS TABLE OF NUMBER"},
		{"TABLE " + table, " (ID INTEGER PRIMARY KEY,V " + root + ")"},
		{"TABLE " + elements, " (ID INTEGER,POS INTEGER,N NUMBER)"},
		{"PROCEDURE " + proc, "(k IN NUMBER,p IN " + array + ") IS BEGIN INSERT INTO " + elements + " VALUES(k,0,p.COUNT); FOR i IN 1..p.COUNT LOOP INSERT INTO " + elements + " VALUES(k,i,p(i)); END LOOP; EXCEPTION WHEN COLLECTION_IS_NULL THEN INSERT INTO " + elements + " VALUES(k,-1,NULL); END;"},
	} {
		if _, err := db.ExecContext(ctx, "CREATE "+ddl.identity+ddl.body); err != nil {
			integrationtest.FailOperation(t, "create client constructor fixture", err)
		}
		created = append(created, ddl.identity)
	}
	var owner string
	if err := db.QueryRowContext(ctx, "SELECT USER FROM DUAL").Scan(&owner); err != nil {
		integrationtest.FailOperation(t, "read constructor owner", err)
	}
	leaf := oceanbase.Object{Schema: owner, Type: child, Fields: []oceanbase.Attribute{
		{Name: "N", Value: oceanbase.Number("12345678901234567890.00123456")}, {Name: "S", Value: "a,b (c) 'q'\\"},
		{Name: "U", Value: "患者😀"}, {Name: "R", Value: oceanbase.Raw{0, 255}},
		{Name: "D", Value: "2026-11-02 03:04:05"}, {Name: "TS", Value: "2026-11-02 03:04:05.123456789"},
	}}
	value := oceanbase.Object{Schema: owner, Type: root, Fields: []oceanbase.Attribute{{Name: "C", Value: leaf}, {Name: "TAG"}}}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,:v); INSERT INTO "+table+" VALUES (2,:v)", sql.Named("v", value)); err != nil {
		integrationtest.FailOperation(t, "store repeated named constructor", err)
	}
	stmt, err := db.PrepareContext(ctx, "INSERT INTO "+table+" VALUES (3,?)")
	if err != nil {
		integrationtest.FailOperation(t, "prepare client constructor", err)
	}
	defer func() { _ = stmt.Close() }()
	if _, err := stmt.ExecContext(ctx, value); err != nil {
		integrationtest.FailOperation(t, "execute prepared client constructor", err)
	}
	var count int
	predicate := `t.V.C.N=12345678901234567890.00123456 AND t.V.C.S='a,b (c) ''q''\' AND t.V.C.U=N'患者😀' AND RAWTOHEX(t.V.C.R)='00FF' AND t.V.C.D=TO_DATE('2026-11-02 03:04:05','YYYY-MM-DD HH24:MI:SS') AND t.V.C.TS=TIMESTAMP '2026-11-02 03:04:05.123456789' AND t.V.TAG IS NULL`
	if err := verification.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" t WHERE "+predicate).Scan(&count); err != nil || count != 3 {
		t.Fatalf("independent constructor storage check: count=%d err=%v", count, err)
	}
	values := oceanbase.Array{Schema: owner, Type: array, Elements: []any{oceanbase.Number("7"), nil, int64(-9)}}
	empty, null := oceanbase.Array{Schema: owner, Type: array}, oceanbase.Array{Schema: owner, Type: array, Null: true}
	if _, err := db.ExecContext(ctx, "CALL "+proc+"(1,?); CALL "+proc+"(2,?); CALL "+proc+"(3,?)", values, empty, null); err != nil {
		integrationtest.FailOperation(t, "PL collection constructors", err)
	}
	rows, err := verification.QueryContext(ctx, "SELECT ID,POS,N FROM "+elements+" ORDER BY ID,POS")
	if err != nil {
		integrationtest.FailOperation(t, "verify PL collection storage", err)
	}
	defer func() { _ = rows.Close() }()
	for _, want := range []struct {
		id, pos int
		n       sql.NullInt64
	}{{1, 0, sql.NullInt64{Int64: 3, Valid: true}}, {1, 1, sql.NullInt64{Int64: 7, Valid: true}}, {1, 2, sql.NullInt64{}}, {1, 3, sql.NullInt64{Int64: -9, Valid: true}}, {2, 0, sql.NullInt64{Int64: 0, Valid: true}}, {3, -1, sql.NullInt64{}}} {
		var id, pos int
		var n sql.NullInt64
		if !rows.Next() {
			t.Fatal("missing collection row", rows.Err())
		}
		if err := rows.Scan(&id, &pos, &n); err != nil || id != want.id || pos != want.pos || n != want.n {
			t.Fatal("collection NULL/empty/order changed", err)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("unexpected collection row", rows.Err())
	}
}
