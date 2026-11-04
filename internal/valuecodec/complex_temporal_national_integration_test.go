//go:build integration

package valuecodec_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

const complexNativeTimestamp = "2026-11-03 01:02:03.123456789"
const complexNativeTimestampType = "TIMESTAMP(9)"
const complexNativeGBK = "gbk"

func TestOracleComplexTemporalNational(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeOracle {
			continue
		}
		for _, scalar := range []string{complexNativeTimestampType, "NVARCHAR2(20)", "NCHAR(4)"} {
			for _, named := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/named=%t", profile.Name, scalar, named), func(t *testing.T) {
					testOracleTemporalNationalArray(t, profile, scalar, named, "", "")
				})
				if scalar != complexNativeTimestampType {
					t.Run(fmt.Sprintf("%s/%s/named=%t/national-utf16", profile.Name, scalar, named), func(t *testing.T) {
						testOracleTemporalNationalArray(t, profile, scalar, named, "", "utf16")
					})
					t.Run(fmt.Sprintf("%s/%s/named=%t/client-gbk", profile.Name, scalar, named), func(t *testing.T) {
						testOracleTemporalNationalArray(t, profile, scalar, named, complexNativeGBK, "utf16")
					})
				}
			}
		}
		t.Run(profile.Name+"/object", func(t *testing.T) { testOracleTimestampObject(t, profile) })
	}
}

func testOracleTemporalNationalArray(t *testing.T, profile integrationtest.Profile, scalar string, named bool, clientCharset, ncharset string) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, profile)
	cfg.Session.NCharset = ncharset
	if clientCharset != "" {
		cfg.Session.Charset = clientCharset
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	observer := integrationtest.OpenProfile(t, profile)
	ctx := integrationtest.TestContext(t)
	base := fmt.Sprintf("OBGTN_%d", time.Now().UnixNano())
	typ, proc, table := base+"A", base+"P", base+"T"
	createTemporalNationalFixture(t, db, "TYPE", typ, " AS TABLE OF "+scalar)
	createTemporalNationalFixture(t, db, "TABLE", table, " (ID NUMBER PRIMARY KEY,N NUMBER,V VARCHAR2(200))")
	value, want, element := any("患者😀𠀀"), "患者😀𠀀", valueformat.TypeNameNVarchar2
	if clientCharset == complexNativeGBK {
		value, want = "患者国标", "患者国标"
	}
	expr := "p(1)"
	switch scalar {
	case complexNativeTimestampType:
		value, want, element = complexNativeTimestamp, complexNativeTimestamp, valueformat.TypeNameTimestamp
		expr = "TO_CHAR(p(1),'YYYY-MM-DD HH24:MI:SS.FF9')"
	case "NCHAR(4)":
		value, want, element = "中文AZ", "中文AZ", valueformat.TypeNameNChar
	}
	body := "(p IN " + typ + ",i IN NUMBER) IS BEGIN IF p IS NULL THEN INSERT INTO " + table + " VALUES(i,-1,NULL); ELSIF p.COUNT=0 THEN INSERT INTO " + table + " VALUES(i,0,NULL); ELSE IF p(2) IS NOT NULL THEN RAISE_APPLICATION_ERROR(-20001,'NULL element changed'); END IF; INSERT INTO " + table + " VALUES(i,p.COUNT," + expr + "); END IF; END;"
	createTemporalNationalFixture(t, db, "PROCEDURE", proc, body)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	stmt, err := conn.PrepareContext(ctx, "BEGIN "+proc+"(?,?); END;")
	if err != nil {
		integrationtest.FailOperation(t, "prepare temporal/national array", err)
	}
	defer func() { _ = stmt.Close() }()
	for id, tc := range []struct {
		elements []any
		null     bool
		count    int
		text     sql.NullString
	}{
		{[]any{value, nil}, false, 2, sql.NullString{String: want, Valid: true}},
		{nil, false, 0, sql.NullString{}},
		{[]any{nil, nil}, false, 2, sql.NullString{}},
		{nil, true, -1, sql.NullString{}},
		{[]any{value, nil}, false, 2, sql.NullString{String: want, Valid: true}},
	} {
		input := oceanbase.Array{ElementType: element, Elements: tc.elements, Null: tc.null}
		if named {
			input.Type = typ
		}
		if _, err := stmt.ExecContext(ctx, input, id); err != nil {
			integrationtest.FailOperation(t, "execute temporal/national array", err)
		}
		var count int
		var text sql.NullString
		if err := observer.QueryRowContext(ctx, "SELECT N,V FROM "+table+" WHERE ID=?", id).Scan(&count, &text); err != nil {
			integrationtest.FailOperation(t, "observe temporal/national array", err)
		}
		if count != tc.count || text != tc.text {
			t.Fatalf("array changed: id=%d count=%d text=%v", id, count, text)
		}
		var output oceanbase.Array
		if _, err := conn.ExecContext(ctx, "DECLARE a "+typ+" := ?; BEGIN ? := a; END;", input, sql.Out{Dest: &output}); err != nil {
			integrationtest.FailOperation(t, "return temporal/national array", err)
		}
		sameElements := reflect.DeepEqual(output.Elements, tc.elements) || len(output.Elements) == 0 && len(tc.elements) == 0
		if output.Null != tc.null || !sameElements {
			t.Fatalf("array output changed: %#v", output)
		}
	}
	createTemporalNationalFixture(t, db, "PROCEDURE", base+"R", "(p IN OUT "+typ+") IS BEGIN p:="+typ+"(NULL,p(1)); END;")
	wantInout := value
	if element == valueformat.TypeNameTimestamp {
		var control string
		if _, err := observer.ExecContext(ctx, "DECLARE a "+typ+" := "+typ+"(TIMESTAMP '"+complexNativeTimestamp+"',NULL); BEGIN "+base+"R(a); ? := TO_CHAR(a(2),'YYYY-MM-DD HH24:MI:SS.FF9'); END;", sql.Out{Dest: &control}); err != nil {
			integrationtest.FailOperation(t, "timestamp INOUT server control", err)
		}
		if control != complexNativeTimestamp {
			t.Fatal("TIMESTAMP constructor control lost nanoseconds", control)
		}
		t.Logf("TIMESTAMP(9) collection constructor INOUT server control=%s", control)
		wantInout = control
		if !named {
			// Anonymous timestamp metadata carries no accuracy; this target defaults direct INOUT to six digits.
			wantInout = "2026-11-03 01:02:03.123457"
			t.Log("anonymous TIMESTAMP INOUT server accuracy is six digits; use the named collection for nanoseconds")
		}
	}
	inout := oceanbase.Array{ElementType: element, Elements: []any{value, nil}}
	if named {
		inout.Type = typ
	}
	if _, err := conn.ExecContext(ctx, "BEGIN "+base+"R(?); END;", sql.Out{Dest: &inout, In: true}); err != nil {
		integrationtest.FailOperation(t, "INOUT temporal/national array", err)
	}
	if !reflect.DeepEqual(inout.Elements, []any{nil, wantInout}) {
		t.Fatalf("array INOUT changed: %#v", inout)
	}
	if err := conn.PingContext(ctx); err != nil {
		integrationtest.FailOperation(t, "reuse temporal/national connection", err)
	}
}

func testOracleTimestampObject(t *testing.T, profile integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, profile)
	observer := integrationtest.OpenProfile(t, profile)
	ctx := integrationtest.TestContext(t)
	base := fmt.Sprintf("OBGTO_%d", time.Now().UnixNano())
	leaf, root, table := base+"L", base+"R", base+"T"
	createTemporalNationalFixture(t, db, "TYPE", leaf, " AS OBJECT (V TIMESTAMP(9),N TIMESTAMP(9))")
	createTemporalNationalFixture(t, db, "TYPE", root, " AS OBJECT (C "+leaf+")")
	createTemporalNationalFixture(t, db, "TABLE", table, " (ID NUMBER PRIMARY KEY,V "+root+")")
	values := []any{complexNativeTimestamp, time.Date(2026, 11, 3, 4, 5, 6, 987654321, time.FixedZone("wall", 3600))}
	wants := []string{complexNativeTimestamp, "2026-11-03 04:05:06.987654321"}
	for id, value := range values {
		child := oceanbase.Object{Type: leaf, Fields: []oceanbase.Attribute{{Name: "V", Value: value}, {Name: "N"}}}
		input := oceanbase.Object{Type: root, Fields: []oceanbase.Attribute{{Name: "C", Value: child}}}
		if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES(?,?)", id, input); err != nil {
			integrationtest.FailOperation(t, "store native timestamp object", err)
		}
		stmt, err := observer.PrepareContext(ctx, "SELECT V FROM "+table+" WHERE ID=?")
		if err != nil {
			integrationtest.FailOperation(t, "prepare timestamp object observer", err)
		}
		var got oceanbase.Object
		err = stmt.QueryRowContext(ctx, id).Scan(&got)
		_ = stmt.Close()
		if err != nil {
			integrationtest.FailOperation(t, "read timestamp object", err)
		}
		if len(got.Fields) != 1 {
			t.Fatal("timestamp root shape changed")
		}
		nested, ok := got.Fields[0].Value.(oceanbase.Object)
		if !ok || len(nested.Fields) != 2 || nested.Fields[0].Value != wants[id] || nested.Fields[1].Value != nil {
			t.Fatal("timestamp wall time, nanos or NULL changed", got)
		}
	}
}

func createTemporalNationalFixture(t *testing.T, db *sql.DB, kind, name, body string) {
	t.Helper()
	if _, err := db.ExecContext(integrationtest.TestContext(t), "CREATE "+kind+" "+name+body); err != nil {
		integrationtest.FailOperation(t, "create temporal/national fixture", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DROP "+kind+" "+name); err != nil {
			integrationtest.FailOperation(t, "drop temporal/national fixture", err)
		}
	})
}
