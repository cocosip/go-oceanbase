//go:build integration

package valuecodec_test

import (
	bytes "bytes"
	sql "database/sql"
	errors "errors"
	fmt "fmt"
	math "math"
	big "math/big"
	reflect "reflect"
	strings "strings"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

const integrationScalarTextPath = "text"

func integrationTestScalarProfile(t *testing.T, p integrationtest.Profile, path string) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.Session.Location = time.UTC
	cfg.Session.ParseTime = false
	cfg.Session.Charset = "utf8mb4"
	if p.Mode == oceanbase.ModeOracle {
		cfg.Session.NCharset = "utf16"
	}
	if path == "client" {
		cfg.PrepareMode = oceanbase.PrepareClient
	} else {
		cfg.PrepareMode = oceanbase.PrepareServer
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid scalar test config")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	table := fmt.Sprintf("obgo_types_%d", time.Now().UnixNano())
	definition := "id INTEGER PRIMARY KEY,n DECIMAL(38,9),v VARCHAR(100),b VARBINARY(100),ts DATETIME(6),d DATE,tm TIME(6),yr YEAR,u BIGINT UNSIGNED,s BIGINT,bf BINARY(4)"
	values := "?,?,?,?,?,?,?,?,?,?,?"
	query := "SELECT n,v,b,ts,d,tm,yr FROM " + table + " WHERE id=1"
	instant := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	args := []any{1, oceanbase.Number("12345678901234567890.123456789"), "中文😀", []byte{0, 255}, instant, integrationScalarDateText, "-838:59:58.999999", 2026}
	args = append(args, uint64(math.MaxUint64), int64(math.MinInt64), []byte{0, 255, 0, 0})
	if p.Mode == oceanbase.ModeOracle {
		definition = "id INTEGER PRIMARY KEY,n NUMBER(38,9),v NVARCHAR2(100),b RAW(100),ts TIMESTAMP(9),tz TIMESTAMP(9) WITH TIME ZONE,ltz TIMESTAMP(9) WITH LOCAL TIME ZONE,ym INTERVAL YEAR(9) TO MONTH,ds INTERVAL DAY(9) TO SECOND(9),dt DATE"
		values = "?,?,?,?,?,?,?,?,?,?"
		query = "SELECT n,v,b,ts,tz,ltz,ym,ds,ROWID FROM " + table + " WHERE id=1"
		instant = instant.Add(789 * time.Nanosecond)
		args = []any{1, oceanbase.Number("12345678901234567890.123456789"), oceanbase.NString("中文😀"), oceanbase.Raw{0, 255}, instant, oceanbase.TimestampTZ("2026-01-02 03:04:05.123456789 -00:30"), oceanbase.TimestampLTZ("2026-01-02 03:04:05.123456789 +00:00"), oceanbase.IntervalYM("-999999999-11"), oceanbase.IntervalDS("-999999999 23:59:59.123456789")}
		args = append(args, time.Date(2024, 2, 29, 12, 34, 56, 0, time.UTC))
	}
	if _, err = db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+" ("+definition+")"); err != nil {
		integrationtest.FailOperation(t, "create scalar table", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop scalar table", err)
		}
	})
	if _, err = db.ExecContext(integrationtest.TestContext(t), "INSERT INTO "+table+" VALUES ("+values+")", args...); err != nil {
		integrationtest.FailOperation(t, "insert scalar values", err)
	}
	var row *sql.Row
	if path == integrationScalarTextPath {
		row = db.QueryRowContext(integrationtest.TestContext(t), query)
	} else {
		stmt, err := db.PrepareContext(integrationtest.TestContext(t), query)
		if err != nil {
			integrationtest.FailOperation(t, "prepare scalar select", err)
		}
		defer func() { _ = stmt.Close() }()
		row = stmt.QueryRowContext(integrationtest.TestContext(t))
	}
	integrationCheckScalarProfileRow(t, p, db, table, row, instant)
	integrationCheckScalarMetadata(t, p, db, table, path)
}

const integrationScalarDateText = "2024-02-29"

func integrationCheckScalarMetadata(t *testing.T, p integrationtest.Profile, db *sql.DB, table, path string) {
	t.Helper()
	query := "SELECT d,ts,u,s,b,bf FROM " + table + " WHERE id=1"
	wantColumns := 6
	wantDate, wantTimestamp := integrationScalarDateText, "DATETIME"
	if p.Mode == oceanbase.ModeOracle {
		query = "SELECT dt,ts FROM " + table + " WHERE id=1"
		wantColumns = 2
		wantDate, wantTimestamp = "2024-02-29 12:34:56", "TIMESTAMP"
	}
	var rows *sql.Rows
	var err error
	if path == integrationScalarTextPath {
		rows, err = db.QueryContext(integrationtest.TestContext(t), query)
	} else {
		stmt, prepareErr := db.PrepareContext(integrationtest.TestContext(t), query)
		if prepareErr != nil {
			integrationtest.FailOperation(t, "prepare scalar metadata select", prepareErr)
		}
		defer func() { _ = stmt.Close() }()
		rows, err = stmt.QueryContext(integrationtest.TestContext(t))
	}
	if err != nil {
		integrationtest.FailOperation(t, "query scalar metadata", err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.ColumnTypes()
	if err != nil || len(columns) != wantColumns {
		t.Fatal("scalar metadata missing", err)
	}
	if columns[0].DatabaseTypeName() != "DATE" || columns[1].DatabaseTypeName() != wantTimestamp {
		t.Fatal("date/timestamp database type names changed", columns[0].DatabaseTypeName(), columns[1].DatabaseTypeName())
	}
	var date, timestamp string
	destinations := []any{&date, &timestamp}
	var unsigned, signed reflect.Value
	var variableBinary, fixedBinary []byte
	if p.Mode == oceanbase.ModeMySQL {
		if columns[2].ScanType() != reflect.TypeFor[uint64]() || columns[3].ScanType() != reflect.TypeFor[int64]() {
			t.Fatal("integer ScanType cannot represent full column range")
		}
		unsigned, signed = reflect.New(columns[2].ScanType()), reflect.New(columns[3].ScanType())
		destinations = append(destinations, unsigned.Interface(), signed.Interface(), &variableBinary, &fixedBinary)
		integrationCheckScalarBinaryMetadata(t, columns[4:])
	}
	if !rows.Next() {
		t.Fatal("date row missing", rows.Err())
	}
	if err = rows.Scan(destinations...); err != nil {
		integrationtest.FailOperation(t, "read scalar metadata values", err)
	}
	if date != wantDate {
		t.Fatal("date time-of-day changed", date)
	}
	if p.Mode == oceanbase.ModeMySQL && (unsigned.Elem().Interface() != uint64(math.MaxUint64) || signed.Elem().Interface() != int64(math.MinInt64)) {
		t.Fatal("integer ScanType lost boundary value")
	}
	if p.Mode == oceanbase.ModeMySQL && (!bytes.Equal(variableBinary, []byte{0, 255}) || !bytes.Equal(fixedBinary, []byte{0, 255, 0, 0})) {
		t.Fatal("binary string bytes or padding changed")
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("date result boundary changed", rows.Err())
	}
}

func integrationCheckScalarBinaryMetadata(t *testing.T, columns []*sql.ColumnType) {
	t.Helper()
	for i, want := range []struct {
		name   string
		length int64
	}{{"VARBINARY", 100}, {"BINARY", 4}} {
		if columns[i].DatabaseTypeName() != want.name || columns[i].ScanType() != reflect.TypeFor[[]byte]() {
			t.Fatal("binary string type metadata changed", columns[i].DatabaseTypeName(), columns[i].ScanType())
		}
		if size, known := columns[i].Length(); !known || size != want.length {
			t.Fatal("binary string byte length changed", size, known)
		}
	}
}

func integrationCheckScalarProfileRow(t *testing.T, p integrationtest.Profile, db *sql.DB, table string, row *sql.Row, instant time.Time) {
	t.Helper()
	var n oceanbase.Number
	var text string
	var raw oceanbase.Raw
	var timestamp string
	if p.Mode == oceanbase.ModeOracle {
		var tz oceanbase.TimestampTZ
		var ltz oceanbase.TimestampLTZ
		var ym oceanbase.IntervalYM
		var ds oceanbase.IntervalDS
		var id oceanbase.RowID
		if err := row.Scan(&n, &text, &raw, &timestamp, &tz, &ltz, &ym, &ds, &id); err != nil {
			integrationtest.FailOperation(t, "read Oracle scalar values", err)
		}
		tzInstant, err := tz.Time()
		if err != nil || !tzInstant.Equal(instant.Add(30*time.Minute)) {
			t.Fatal("timestamp offset/fraction lost", err)
		}
		ltzInstant, err := ltz.Time()
		if err != nil || !ltzInstant.Equal(instant) || ym != "-999999999-11" || ds != "-999999999 23:59:59.123456789" {
			t.Fatal("LTZ or interval changed", err)
		}
		var found int
		if err = db.QueryRowContext(integrationtest.TestContext(t), "SELECT id FROM "+table+" WHERE ROWID=?", id).Scan(&found); err != nil || found != 1 {
			integrationtest.FailOperation(t, "ROWID binding", err)
		}
	} else {
		var date, duration string
		var year int
		if err := row.Scan(&n, &text, &raw, &timestamp, &date, &duration, &year); err != nil {
			integrationtest.FailOperation(t, "read MySQL scalar values", err)
		}
		if date != integrationScalarDateText || duration != "-838:59:58.999999" || year != 2026 {
			t.Fatal("date/TIME/YEAR changed")
		}
	}
	gotNumber, ok := new(big.Rat).SetString(string(n))
	wantNumber, _ := new(big.Rat).SetString("12345678901234567890.123456789")
	gotTime, err := time.Parse("2006-01-02 15:04:05", timestamp)
	if !ok || gotNumber.Cmp(wantNumber) != 0 || text != "中文😀" || len(raw) != 2 || raw[0] != 0 || raw[1] != 255 || err != nil || !gotTime.Equal(instant) {
		t.Fatal("scalar precision/text/binary changed")
	}
}

func integrationTestComplexProfile(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.PrepareMode = oceanbase.PrepareServer
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid complex type configuration")
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	if p.Mode == oceanbase.ModeMySQL {
		_, err = db.ExecContext(integrationtest.TestContext(t), "SELECT ?", oceanbase.Object{Type: "T"})
		if !errors.Is(err, oceanbase.ErrUnsupported) {
			integrationtest.FailOperation(t, "reject Oracle complex type in MySQL compatibility mode", err)
		}
		return
	}
	base := fmt.Sprintf("OBGC_%d", time.Now().UnixNano())
	obj, arr, lob, table := base+"_O", base+"_A", base+"_L", base+"_T"
	var created []string
	defer func() {
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TYPE "+created[i]); err != nil {
				integrationtest.FailOperation(t, "drop complex type fixture", err)
			}
		}
	}()
	for _, ddl := range []struct{ name, body string }{{obj, "OBJECT(N NUMBER(38,8),B RAW(64),D DATE)"}, {arr, "TABLE OF " + obj}, {lob, "OBJECT(N NUMBER,B BLOB)"}} {
		if _, err = db.ExecContext(integrationtest.TestContext(t), "CREATE TYPE "+ddl.name+" AS "+ddl.body); err != nil {
			integrationtest.FailOperation(t, "create complex type fixture", err)
		}
		created = append(created, ddl.name)
	}
	t.Run("nested-table-storage", func(t *testing.T) { integrationTestNestedTableStorage(t, db, base, arr) })
	if _, err = db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+" (ID INTEGER,V "+obj+",L "+lob+")"); err != nil {
		integrationtest.FailOperation(t, "create complex table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop complex table", err)
		}
	}()
	tr, err := db.BeginTx(integrationtest.TestContext(t), nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin complex transaction", err)
	}
	defer func() { _ = tr.Rollback() }()
	const number = "12345678901234567890.00123456"
	const date = "2025-10-17 12:34:56"
	value := oceanbase.Object{Type: obj, Fields: []oceanbase.Attribute{{Name: "N", Value: oceanbase.Number(number)}, {Name: "B", Value: oceanbase.Raw{0, 255}}, {Name: "D", Value: date}}}
	other := oceanbase.Object{Type: obj, Fields: []oceanbase.Attribute{{Name: "N", Value: oceanbase.Number("-42.5")}, {Name: "B", Value: oceanbase.Raw{255, 0}}, {Name: "D", Value: date}}}
	array := oceanbase.Array{Type: arr, Elements: []any{value, nil, other}}
	if _, err = tr.ExecContext(integrationtest.TestContext(t), "INSERT INTO "+table+" VALUES(?,?,"+lob+"(7,TO_BLOB(HEXTORAW('00FF'))))", 1, value); err != nil {
		integrationtest.FailOperation(t, "bind native object and collection", err)
	}
	var got oceanbase.Object
	var items oceanbase.Array
	var withLOB oceanbase.Object
	if err = tr.QueryRowContext(integrationtest.TestContext(t), "SELECT V,L FROM "+table+" WHERE ID=?", 1).Scan(&got, &withLOB); err != nil {
		integrationtest.FailOperation(t, "scan native complex types", err)
	}
	var nullElement int
	if _, err = tr.ExecContext(integrationtest.TestContext(t), "DECLARE a "+arr+" := ?; BEGIN ? := CASE WHEN a(2) IS NULL THEN 1 ELSE 0 END; ? := a; END;", array, sql.Out{Dest: &nullElement}, sql.Out{Dest: &items}); err != nil {
		integrationtest.FailOperation(t, "round-trip native collection through PL/SQL", err)
	}
	integrationAssertComplexObject(t, got, obj, number, date, []byte{0, 255})
	if items.Type != arr || items.Schema != got.Schema || len(items.Elements) != 3 {
		t.Fatal("collection identity, input NULL or order changed")
	}
	integrationAssertCollectionNullElement(t, items.Elements[1], nullElement, obj, got.Schema)
	for _, index := range []int{0, 2} {
		v, ok := items.Elements[index].(oceanbase.Object)
		if !ok || v.Schema != got.Schema {
			t.Fatal("collection element lost its descriptor")
		}
		wantNumber, wantRaw := number, []byte{0, 255}
		if index == 2 {
			wantNumber, wantRaw = "-42.5", []byte{255, 0}
		}
		integrationAssertComplexObject(t, v, obj, wantNumber, date, wantRaw)
	}
	if len(withLOB.Fields) != 2 {
		t.Fatal("nested LOB fields missing")
	}
	raw, ok := withLOB.Fields[1].Value.(oceanbase.Raw)
	if !ok || !bytes.Equal(raw, []byte{0, 255}) {
		t.Fatal("nested BLOB bytes changed")
	}
	var out oceanbase.Object
	if _, err = tr.ExecContext(integrationtest.TestContext(t), "BEGIN ? := "+obj+"(1,HEXTORAW('00FF'),DATE '2025-10-17'); END;", sql.Out{Dest: &out}); err != nil {
		integrationtest.FailOperation(t, "native object output", err)
	}
	integrationAssertComplexObject(t, out, obj, "1", "2025-10-17 00:00:00", []byte{0, 255})
	if out.Schema != got.Schema {
		t.Fatal("object output schema changed")
	}
}

func integrationAssertCollectionNullElement(t *testing.T, element any, predicate int, obj, schema string) {
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

func integrationAssertComplexObject(t *testing.T, value oceanbase.Object, name, number, date string, raw []byte) {
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

func integrationTestNestedTableStorage(t *testing.T, db *sql.DB, base, arr string) {
	t.Helper()
	storage := base + "_S"
	_, err := db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+storage+" (A "+arr+")")
	var server *oceanbase.Error
	if errors.As(err, &server) && server.Code == 600 && server.SQLState == "0A000" && strings.Contains(server.Message, "-4007, nested table type not supported") {
		t.Skip("server rejects nested-table column storage; PL/SQL collection binding tested separately")
	}
	if err != nil {
		integrationtest.FailOperation(t, "create nested-table storage", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+storage); err != nil {
			integrationtest.FailOperation(t, "drop nested-table storage", err)
		}
	}()
	if _, err := db.ExecContext(integrationtest.TestContext(t), "INSERT INTO "+storage+" VALUES ("+arr+"())"); err != nil {
		integrationtest.FailOperation(t, "insert empty stored collection", err)
	}
	var got oceanbase.Array
	if err := db.QueryRowContext(integrationtest.TestContext(t), "SELECT A FROM "+storage+" WHERE ROWNUM=?", 1).Scan(&got); err != nil {
		integrationtest.FailOperation(t, "read stored collection", err)
	}
	if got.Type != arr || got.Null || len(got.Elements) != 0 {
		t.Fatal("stored collection changed")
	}
}

func integrationTestMySQLCharset(t *testing.T, p integrationtest.Profile, charset, text string) {
	t.Helper()
	for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
		cfg := integrationtest.ProfileConfig(t, p)
		cfg.Session.Charset, cfg.PrepareMode = charset, prepare
		connector, err := oceanbase.NewConnector(cfg)
		if err != nil {
			integrationtest.FailOperation(t, "charset connector", err)
		}
		db := sql.OpenDB(connector)
		func() {
			defer func() { _ = db.Close() }()
			ctx := integrationtest.TestContext(t)
			c, err := db.Conn(ctx)
			if err != nil {
				integrationtest.FailOperation(t, "charset connection", err)
			}
			defer func() { _ = c.Close() }()
			for _, mode := range []string{"", "NO_BACKSLASH_ESCAPES"} {
				if _, err = c.ExecContext(ctx, "SET SESSION sql_mode='"+mode+"'"); err != nil {
					integrationtest.FailOperation(t, "charset SQL mode", err)
				}
				var got string
				if err = c.QueryRowContext(ctx, "SELECT ?", text).Scan(&got); err != nil {
					integrationtest.FailOperation(t, "charset round trip", err)
				}
				if got != text {
					t.Fatalf("charset=%s prepare=%d mode=%s: got=%q want=%q", charset, prepare, mode, got, text)
				}
			}
		}()
	}
}
