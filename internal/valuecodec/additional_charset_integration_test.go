//go:build integration

package valuecodec_test

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestMySQLAdditionalCharsetColumns(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeMySQL {
			continue
		}
		for _, tc := range []struct{ name, text, raw string }{
			{"cp850", "café", "63616682"},
			{"hp8", "ÄÖé", "d8dac5"},
			{"macroman", "ÄéΩ", "808ebd"},
			{"swe7", "ÅÄÖåäö", "5d5b5c7d7b7c"},
			{"ujis", "日本語ｱ〜˘\ue000\ue3ac", "c6fccbdcb8ec8eb1a1c18fa2aff5a18ff5a1"},
			{"cp932", "日本語ｱ～①髙\ue000", "93fa967b8ceab181608740fbfcf040"},
			{"eucjpms", "日本語ｱ～①髙\ue000\ue3ac", "c6fccbdcb8ec8eb1a1c1ada18ff4fbf5a18ff5a1"},
		} {
			for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				t.Run(fmt.Sprintf("%s/%s/%d", profile.Name, tc.name, prepare), func(t *testing.T) {
					testAdditionalCharsetColumn(t, profile, prepare, tc.name, tc.text, tc.raw)
				})
			}
		}
	}
}

func testAdditionalCharsetColumn(t *testing.T, profile integrationtest.Profile, prepare oceanbase.PrepareMode, name, text, raw string) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, profile)
	cfg.Session.Charset, cfg.PrepareMode = charsetUTF8MB4, prepare
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx := integrationtest.TestContext(t)
	c, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin charset column session", err)
	}
	defer func() { _ = c.Close() }()
	table := fmt.Sprintf("OBGCS_%d", time.Now().UnixNano())
	if _, err := c.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(200) CHARACTER SET "+name+" COLLATE "+name+"_bin)"); err != nil {
		integrationtest.FailOperation(t, "create charset column", err)
	}
	defer func() {
		if _, err := c.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop charset column", err)
		}
	}()
	for _, value := range []struct {
		id    int
		value any
	}{{1, text}, {2, nil}} {
		if _, err := c.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?)", value.id, value.value); err != nil {
			integrationtest.FailOperation(t, "store charset column", err)
		}
	}
	encoded, err := charset.Encode(name, text)
	if err != nil || !strings.EqualFold(hex.EncodeToString(encoded), raw) {
		t.Fatal("encoder differs from frozen server bytes", err)
	}
	if _, err := c.ExecContext(ctx, "INSERT INTO "+table+" VALUES (3,CONVERT(CAST(? AS BINARY) USING "+name+"))", encoded); err != nil {
		integrationtest.FailOperation(t, "store encoded charset bytes", err)
	}
	if _, err := c.ExecContext(ctx, "SET character_set_results="+name); err != nil {
		integrationtest.FailOperation(t, "set legacy result charset", err)
	}
	for _, bound := range []bool{false, true} {
		query := "SELECT V,HEX(V) FROM " + table + " WHERE ID>=1 ORDER BY ID"
		var args []any
		if bound {
			query = "SELECT V,HEX(V) FROM " + table + " WHERE ID>=? ORDER BY ID"
			args = []any{1}
		}
		rows, err := c.QueryContext(ctx, query, args...)
		if err != nil {
			integrationtest.FailOperation(t, "read legacy charset results", err)
		}
		checkAdditionalCharsetRows(t, rows, text, raw)
	}
	observer := integrationtest.OpenProfile(t, profile)
	var got string
	if err := observer.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=3").Scan(&got); err != nil || got != text {
		t.Fatal("independent UTF8 reader mismatch", err)
	}
}

func checkAdditionalCharsetRows(t *testing.T, rows *sql.Rows, text, raw string) {
	t.Helper()
	defer func() { _ = rows.Close() }()
	for id := 1; id <= 3; id++ {
		if !rows.Next() {
			t.Fatal("missing charset row", id, rows.Err())
		}
		var got, stored sql.NullString
		if err := rows.Scan(&got, &stored); err != nil {
			t.Fatal(err)
		}
		if id == 2 {
			if got.Valid || stored.Valid {
				t.Fatal("charset NULL changed")
			}
		} else if !got.Valid || got.String != text || !stored.Valid || !strings.EqualFold(stored.String, raw) {
			t.Fatalf("charset storage mismatch row=%d: %q %q", id, got.String, stored.String)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("unexpected charset rows", rows.Err())
	}
}
