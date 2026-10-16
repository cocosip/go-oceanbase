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
	if _, err = db.ExecContext(testContext(t), "CREATE TABLE "+table+" (ID INTEGER,V "+obj+",A "+arr+",L "+lob+")"); err != nil {
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
	value := oceanbase.Object{Type: obj, Fields: []oceanbase.Attribute{{Name: "N", Value: oceanbase.Number(number)}, {Name: "B", Value: oceanbase.Raw{0, 255}}, {Name: "D", Value: "2025-10-17 12:34:56"}}}
	array := oceanbase.Array{Type: arr, Elements: []any{value, nil, value}}
	if _, err = tr.ExecContext(testContext(t), "INSERT INTO "+table+" VALUES(?,?,?,"+lob+"(7,TO_BLOB(HEXTORAW('00FF'))))", 1, value, array); err != nil {
		failOperation(t, "bind native object and collection", err)
	}
	var got oceanbase.Object
	var items oceanbase.Array
	var withLOB oceanbase.Object
	if err = tr.QueryRowContext(testContext(t), "SELECT V,A,L FROM "+table+" WHERE ID=?", 1).Scan(&got, &items, &withLOB); err != nil {
		failOperation(t, "scan native complex types", err)
	}
	assertComplexObject(t, got, obj, number)
	if len(items.Elements) != 3 || items.Elements[1] != nil {
		t.Fatal("collection NULL or order changed")
	}
	for _, index := range []int{0, 2} {
		v, ok := items.Elements[index].(oceanbase.Object)
		if !ok {
			t.Fatal("collection element lost its descriptor")
		}
		assertComplexObject(t, v, obj, number)
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
	if out.Type != obj || len(out.Fields) != 3 {
		t.Fatal("object output descriptor changed")
	}
}

func assertComplexObject(t *testing.T, value oceanbase.Object, name, number string) {
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
	if !ok || !bytes.Equal(b, []byte{0, 255}) {
		t.Fatal("RAW changed")
	}
	d, ok := value.Fields[2].Value.(string)
	if !ok || !strings.HasPrefix(d, "2025-10-17 12:34:56") {
		t.Fatal("DATE changed")
	}
}
