//go:build integration

package valuecodec_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestOracleNamedLOBContent(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeOracle {
			continue
		}
		for _, combined := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/combined=%t", profile.Name, combined), func(t *testing.T) {
				cfg := integrationtest.ProfileConfig(t, profile)
				cfg.Execution.PrepareExecute = combined
				connector, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				db.SetMaxOpenConns(1)
				defer func() { _ = db.Close() }()
				for _, scalar := range []string{"BLOB", testLOBClob} {
					for _, shape := range []string{"object", testLOBNested, testLOBCollection} {
						t.Run(scalar+"/"+shape, func(t *testing.T) { verifyNamedLOBContent(t, db, scalar, shape) })
					}
				}
			})
		}
	}
}

func verifyNamedLOBContent(t *testing.T, db *sql.DB, scalar, shape string) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	base := fmt.Sprintf("OBGL_%d", time.Now().UnixNano())
	leaf, proc := base+"L", base+"P"
	var created []string
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := db.ExecContext(cleanup, "DROP "+created[i]); err != nil {
				t.Errorf("drop LOB fixture: %v", err)
			}
		}
	}()
	create := func(kind, name, body string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "CREATE "+kind+" "+name+body); err != nil {
			integrationtest.FailOperation(t, "create named LOB fixture", err)
		}
		created = append(created, kind+" "+name)
	}
	create("TYPE", leaf, " AS OBJECT (A "+scalar+", N "+scalar+")")
	member, nullMember := "p.A", "p.N"
	root := leaf
	switch shape {
	case testLOBNested:
		root = base + "R"
		create("TYPE", root, " AS OBJECT (C "+leaf+")")
		member, nullMember = "p.C.A", "p.C.N"
	case testLOBCollection:
		root = base + "R"
		create("TYPE", root, " AS TABLE OF "+scalar)
		member, nullMember = "p(1)", "p(2)"
	}
	expression := "RAWTOHEX(DBMS_LOB.SUBSTR(" + member + ", 32, 1))"
	if scalar == testLOBClob {
		expression = "DBMS_LOB.SUBSTR(" + member + ", 32, 1)"
	}
	create("PROCEDURE", proc, "(p IN "+root+", n OUT NUMBER, v OUT VARCHAR2) IS BEGIN IF "+nullMember+" IS NOT NULL THEN RAISE_APPLICATION_ERROR(-20001,'NULL member changed'); END IF; n := DBMS_LOB.GETLENGTH("+member+"); v := "+expression+"; END;")
	var owner string
	if err := db.QueryRowContext(ctx, "SELECT USER FROM DUAL").Scan(&owner); err != nil {
		integrationtest.FailOperation(t, "read named LOB owner", err)
	}
	for _, large := range []bool{false, true} {
		data, text := []byte{0, 1, 92, 39, 128, 255}, "患者😀\\'"
		if large {
			data, text = bytes.Repeat([]byte{0, 128, 255}, 30003), strings.Repeat("中", 30003)
		}
		var content any = data
		length, prefix := int64(len(data)), fmt.Sprintf("%X", data[:min(32, len(data))])
		if scalar == testLOBClob {
			content, length = text, int64(utf8.RuneCountInString(text))
			prefix = string([]rune(text)[:min(32, utf8.RuneCountInString(text))])
		}
		var value any = oceanbase.Object{Schema: owner, Type: leaf, Fields: []oceanbase.Attribute{{Name: "A", Value: content}, {Name: "N"}}}
		switch shape {
		case testLOBNested:
			value = oceanbase.Object{Schema: owner, Type: root, Fields: []oceanbase.Attribute{{Name: "C", Value: value}}}
		case testLOBCollection:
			value = oceanbase.Array{Schema: owner, Type: root, Elements: []any{content, nil}}
		}
		var gotLength int64
		var gotPrefix string
		if _, err := db.ExecContext(ctx, "BEGIN "+proc+"(?,?,?); END;", value, oceanbase.Out{Dest: &gotLength}, oceanbase.Out{Dest: &gotPrefix}); err != nil {
			integrationtest.FailOperation(t, "PL reads named LOB content", err)
		}
		if gotLength != length || gotPrefix != prefix {
			t.Fatal("named LOB content or length changed")
		}
	}
}
