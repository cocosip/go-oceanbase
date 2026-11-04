//go:build integration

package valuecodec_test

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

const bitmapMembers = "1,2,4294967295"

func TestMySQLRoaringBitmap(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(profile.Name, func(t *testing.T) {
			db := integrationtest.OpenProfile(t, profile)
			ctx := integrationtest.TestContext(t)
			table := fmt.Sprintf("OBGBM_%d", time.Now().UnixNano())
			if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V ROARINGBITMAP)"); err != nil {
				integrationtest.FailOperation(t, "create bitmap", err)
			}
			defer func() {
				if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
					integrationtest.FailOperation(t, "drop bitmap", err)
				}
			}()
			for id, value := range []any{bitmapMembers, "", nil, "18446744073709551615"} {
				if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,rb_from_string(?))", id+1, value); err != nil {
					integrationtest.FailOperation(t, "insert bitmap", err)
				}
			}
			var first []byte
			for id, want := range []any{bitmapMembers, "", nil, "18446744073709551615"} {
				rows, err := db.QueryContext(ctx, "SELECT V,rb_to_string(V) FROM "+table+" WHERE ID=?", id+1)
				if err != nil {
					t.Fatal(err)
				}
				columns, err := rows.ColumnTypes()
				if err != nil || columns[0].DatabaseTypeName() != "BLOB" || columns[0].ScanType() != reflect.TypeFor[[]byte]() {
					t.Fatal("bitmap metadata changed", err)
				}
				if !rows.Next() {
					t.Fatal("missing bitmap", rows.Err())
				}
				var raw []byte
				var got any
				if err := rows.Scan(&raw, &got); err != nil {
					t.Fatal(err)
				}
				if got != want || want == nil && raw != nil || want != nil && len(raw) == 0 {
					t.Fatal("bitmap NULL/empty/uint64 changed", got)
				}
				if id == 0 {
					first = raw
				}
				if rows.Next() || rows.Err() != nil {
					t.Fatal(rows.Err())
				}
				if err := rows.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (5,rb_build_varbinary(?))", first); err != nil {
				integrationtest.FailOperation(t, "rebind bitmap bytes", err)
			}
			var raw []byte
			var text string
			if err := db.QueryRowContext(ctx, "SELECT V,rb_to_string(V) FROM "+table+" WHERE ID=5").Scan(&raw, &text); err != nil || !bytes.Equal(raw, first) || text != bitmapMembers {
				t.Fatal("bitmap byte round trip changed", err)
			}
			t.Run("direct-bytes", func(t *testing.T) {
				_, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (6,?)", first)
				if integrationtest.OnlyServerErrorCode(err, 1235) {
					t.Skip("B11: target rejects direct bytes; rb_build_varbinary round trip passed")
				}
				if err != nil {
					integrationtest.FailOperation(t, "direct bitmap bytes", err)
				}
			})
		})
	}
}
