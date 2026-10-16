//go:build integration

package integration

import (
	"database/sql"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestAllScalarTypes(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			for _, path := range []string{"text", "server", "client"} {
				t.Run(path, func(t *testing.T) { testScalarProfile(t, p, path) })
			}
		})
	}
}

func testScalarProfile(t *testing.T, p profile, path string) {
	t.Helper()
	cfg := profileConfig(t, p)
	cfg.Session.Location = time.UTC
	cfg.Session.ParseTime = false
	cfg.Session.Charset = "utf8mb4"
	if p.mode == oceanbase.ModeOracle {
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
	definition := "id INTEGER PRIMARY KEY,n DECIMAL(38,9),v VARCHAR(100),b VARBINARY(100),ts DATETIME(6),d DATE,tm TIME(6),yr YEAR"
	values := "?,?,?,?,?,?,?,?"
	query := "SELECT n,v,b,ts,d,tm,yr FROM " + table + " WHERE id=1"
	instant := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	args := []any{1, oceanbase.Number("12345678901234567890.123456789"), "中文😀", []byte{0, 255}, instant, "2024-02-29", "-838:59:59.999999", 2026}
	if p.mode == oceanbase.ModeOracle {
		definition = "id INTEGER PRIMARY KEY,n NUMBER(38,9),v NVARCHAR2(100),b RAW(100),ts TIMESTAMP(9),tz TIMESTAMP(9) WITH TIME ZONE,ltz TIMESTAMP(9) WITH LOCAL TIME ZONE,ym INTERVAL YEAR(9) TO MONTH,ds INTERVAL DAY(9) TO SECOND(9)"
		values = "?,?,?,?,?,?,?,?,?"
		query = "SELECT n,v,b,ts,tz,ltz,ym,ds,ROWID FROM " + table + " WHERE id=1"
		instant = instant.Add(789 * time.Nanosecond)
		args = []any{1, oceanbase.Number("12345678901234567890.123456789"), oceanbase.NString("中文😀"), oceanbase.Raw{0, 255}, instant, oceanbase.TimestampTZ("2026-01-02 03:04:05.123456789 -00:30"), oceanbase.TimestampLTZ("2026-01-02 03:04:05.123456789 +00:00"), oceanbase.IntervalYM("-999999999-11"), oceanbase.IntervalDS("-999999999 23:59:59.123456789")}
	}
	if _, err = db.ExecContext(testContext(t), "CREATE TABLE "+table+" ("+definition+")"); err != nil {
		failOperation(t, "create scalar table", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop scalar table", err)
		}
	})
	if _, err = db.ExecContext(testContext(t), "INSERT INTO "+table+" VALUES ("+values+")", args...); err != nil {
		failOperation(t, "insert scalar values", err)
	}
	var row *sql.Row
	if path == "text" {
		row = db.QueryRowContext(testContext(t), query)
	} else {
		stmt, err := db.PrepareContext(testContext(t), query)
		if err != nil {
			failOperation(t, "prepare scalar select", err)
		}
		defer func() { _ = stmt.Close() }()
		row = stmt.QueryRowContext(testContext(t))
	}
	checkScalarProfileRow(t, p, db, table, row, instant)
}

func checkScalarProfileRow(t *testing.T, p profile, db *sql.DB, table string, row *sql.Row, instant time.Time) {
	t.Helper()
	var n oceanbase.Number
	var text string
	var raw oceanbase.Raw
	var timestamp string
	if p.mode == oceanbase.ModeOracle {
		var tz oceanbase.TimestampTZ
		var ltz oceanbase.TimestampLTZ
		var ym oceanbase.IntervalYM
		var ds oceanbase.IntervalDS
		var id oceanbase.RowID
		if err := row.Scan(&n, &text, &raw, &timestamp, &tz, &ltz, &ym, &ds, &id); err != nil {
			failOperation(t, "read Oracle scalar values", err)
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
		if err = db.QueryRowContext(testContext(t), "SELECT id FROM "+table+" WHERE ROWID=?", id).Scan(&found); err != nil || found != 1 {
			failOperation(t, "ROWID binding", err)
		}
	} else {
		var date, duration string
		var year int
		if err := row.Scan(&n, &text, &raw, &timestamp, &date, &duration, &year); err != nil {
			failOperation(t, "read MySQL scalar values", err)
		}
		if date != "2024-02-29" || duration != "-838:59:59.999999" || year != 2026 {
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
