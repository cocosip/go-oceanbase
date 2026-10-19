//go:build integration

package integration

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestComplexTypes(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testComplexProfile(t, p) })
	}
}

func testComplexProfile(t *testing.T, p profile) {
	t.Helper()
	cfg := profileConfig(t, p)
	cfg.PrepareMode = oceanbase.PrepareServer
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid complex type configuration")
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	if p.mode == oceanbase.ModeMySQL {
		_, err = db.ExecContext(testContext(t), "SELECT ?", oceanbase.Object{Type: "T"})
		if !errors.Is(err, oceanbase.ErrUnsupported) {
			failOperation(t, "reject Oracle complex type in MySQL compatibility mode", err)
		}
		return
	}
	base := fmt.Sprintf("OBGC_%d", time.Now().UnixNano())
	obj, arr, lob, table := base+"_O", base+"_A", base+"_L", base+"_T"
	var created []string
	defer func() {
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := db.ExecContext(testContext(t), "DROP TYPE "+created[i]); err != nil {
				failOperation(t, "drop complex type fixture", err)
			}
		}
	}()
	for _, ddl := range []struct{ name, body string }{{obj, "OBJECT(N NUMBER(38,8),B RAW(64),D DATE)"}, {arr, "TABLE OF " + obj}, {lob, "OBJECT(N NUMBER,B BLOB)"}} {
		if _, err = db.ExecContext(testContext(t), "CREATE TYPE "+ddl.name+" AS "+ddl.body); err != nil {
			failOperation(t, "create complex type fixture", err)
		}
		created = append(created, ddl.name)
	}
	t.Run("nested-table-storage", func(t *testing.T) { testNestedTableStorage(t, db, base, arr) })
	if _, err = db.ExecContext(testContext(t), "CREATE TABLE "+table+" (ID INTEGER,V "+obj+",L "+lob+")"); err != nil {
		failOperation(t, "create complex table", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop complex table", err)
		}
	}()
	tr, err := db.BeginTx(testContext(t), nil)
	if err != nil {
		failOperation(t, "begin complex transaction", err)
	}
	defer func() { _ = tr.Rollback() }()
	const number = "12345678901234567890.00123456"
	const date = "2025-10-17 12:34:56"
	value := oceanbase.Object{Type: obj, Fields: []oceanbase.Attribute{{Name: "N", Value: oceanbase.Number(number)}, {Name: "B", Value: oceanbase.Raw{0, 255}}, {Name: "D", Value: date}}}
	other := oceanbase.Object{Type: obj, Fields: []oceanbase.Attribute{{Name: "N", Value: oceanbase.Number("-42.5")}, {Name: "B", Value: oceanbase.Raw{255, 0}}, {Name: "D", Value: date}}}
	array := oceanbase.Array{Type: arr, Elements: []any{value, nil, other}}
	if _, err = tr.ExecContext(testContext(t), "INSERT INTO "+table+" VALUES(?,?,"+lob+"(7,TO_BLOB(HEXTORAW('00FF'))))", 1, value); err != nil {
		failOperation(t, "bind native object and collection", err)
	}
	var got oceanbase.Object
	var items oceanbase.Array
	var withLOB oceanbase.Object
	if err = tr.QueryRowContext(testContext(t), "SELECT V,L FROM "+table+" WHERE ID=?", 1).Scan(&got, &withLOB); err != nil {
		failOperation(t, "scan native complex types", err)
	}
	var nullElement int
	if _, err = tr.ExecContext(testContext(t), "DECLARE a "+arr+" := ?; BEGIN ? := CASE WHEN a(2) IS NULL THEN 1 ELSE 0 END; ? := a; END;", array, sql.Out{Dest: &nullElement}, sql.Out{Dest: &items}); err != nil {
		failOperation(t, "round-trip native collection through PL/SQL", err)
	}
	assertComplexObject(t, got, obj, number, date, []byte{0, 255})
	if items.Type != arr || items.Schema != got.Schema || len(items.Elements) != 3 {
		t.Fatal("collection identity, input NULL or order changed")
	}
	assertCollectionNullElement(t, items.Elements[1], nullElement, obj, got.Schema)
	for _, index := range []int{0, 2} {
		v, ok := items.Elements[index].(oceanbase.Object)
		if !ok || v.Schema != got.Schema {
			t.Fatal("collection element lost its descriptor")
		}
		wantNumber, wantRaw := number, []byte{0, 255}
		if index == 2 {
			wantNumber, wantRaw = "-42.5", []byte{255, 0}
		}
		assertComplexObject(t, v, obj, wantNumber, date, wantRaw)
	}
	if len(withLOB.Fields) != 2 {
		t.Fatal("nested LOB fields missing")
	}
	raw, ok := withLOB.Fields[1].Value.(oceanbase.Raw)
	if !ok || !bytes.Equal(raw, []byte{0, 255}) {
		t.Fatal("nested BLOB bytes changed")
	}
	var out oceanbase.Object
	if _, err = tr.ExecContext(testContext(t), "BEGIN ? := "+obj+"(1,HEXTORAW('00FF'),DATE '2025-10-17'); END;", sql.Out{Dest: &out}); err != nil {
		failOperation(t, "native object output", err)
	}
	assertComplexObject(t, out, obj, "1", "2025-10-17 00:00:00", []byte{0, 255})
	if out.Schema != got.Schema {
		t.Fatal("object output schema changed")
	}
}

func assertComplexObject(t *testing.T, value oceanbase.Object, name, number, date string, raw []byte) {
	t.Helper()
	if value.Type != name || value.Schema == "" || len(value.Fields) != 3 || value.Fields[0].Name != "N" || value.Fields[1].Name != "B" || value.Fields[2].Name != "D" {
		t.Fatal("object identity or field order changed")
	}
	n, ok := value.Fields[0].Value.(oceanbase.Number)
	if !ok {
		t.Fatal("NUMBER lost its exact representation")
	}
	want, _ := new(big.Rat).SetString(number)
	got, ok := new(big.Rat).SetString(string(n))
	if !ok || got.Cmp(want) != 0 {
		t.Fatal("NUMBER precision changed")
	}
	b, ok := value.Fields[1].Value.(oceanbase.Raw)
	if !ok || !bytes.Equal(b, raw) {
		t.Fatal("RAW changed")
	}
	d, ok := value.Fields[2].Value.(string)
	if !ok || !complexDateMatches(d, date) {
		t.Fatal("DATE changed")
	}
}

func complexDateMatches(value, want string) bool {
	return value == want || value == want+".000000"
}

func TestComplexDateRepresentation(t *testing.T) {
	const date = "2025-10-17 12:34:56"
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{date, true},
		{date + ".000000", true},
		{date + ".000001", false},
		{date + ".000000garbage", false},
		{date + ".0000000", false},
		{"2025-10-17 12:34:57", false},
		{"2025-10-17", false},
	} {
		if got := complexDateMatches(tc.value, date); got != tc.want {
			t.Errorf("complex DATE %q: got %v, want %v", tc.value, got, tc.want)
		}
	}
}

func testNestedTableStorage(t *testing.T, db *sql.DB, base, arr string) {
	t.Helper()
	storage := base + "_S"
	_, err := db.ExecContext(testContext(t), "CREATE TABLE "+storage+" (A "+arr+")")
	var server *oceanbase.Error
	if errors.As(err, &server) && server.Code == 600 && server.SQLState == "0A000" && strings.Contains(server.Message, "-4007, nested table type not supported") {
		t.Skip("server rejects nested-table column storage; PL/SQL collection binding tested separately")
	}
	if err != nil {
		failOperation(t, "create nested-table storage", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+storage); err != nil {
			failOperation(t, "drop nested-table storage", err)
		}
	}()
	if _, err := db.ExecContext(testContext(t), "INSERT INTO "+storage+" VALUES ("+arr+"())"); err != nil {
		failOperation(t, "insert empty stored collection", err)
	}
	var got oceanbase.Array
	if err := db.QueryRowContext(testContext(t), "SELECT A FROM "+storage+" WHERE ROWNUM=?", 1).Scan(&got); err != nil {
		failOperation(t, "read stored collection", err)
	}
	if got.Type != arr || got.Null || len(got.Elements) != 0 {
		t.Fatal("stored collection changed")
	}
}

func assertCollectionNullElement(t *testing.T, element any, predicate int, obj, schema string) {
	t.Helper()
	// OceanBase 4.4 returns a NULL object element as an object with NULL attributes, as does JDBC.
	if element != nil {
		if predicate != 0 {
			t.Fatal("server NULL object representation is inconsistent")
		}
		n, ok := element.(oceanbase.Object)
		if !ok || n.Type != obj || n.Schema != schema || len(n.Fields) != 3 || n.Fields[0].Name != "N" || n.Fields[1].Name != "B" || n.Fields[2].Name != "D" {
			t.Fatal("NULL object identity changed")
		}
		for _, f := range n.Fields {
			if f.Value != nil {
				t.Fatal("NULL object attribute changed")
			}
		}
	} else if predicate != 1 {
		t.Fatal("server NULL element predicate is inconsistent")
	}
}
