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

func TestPrepareRoundTrip(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			db := openProfile(t, p)
			ctx := testContext(t)
			table := fmt.Sprintf("obgo_ps_%d", time.Now().UnixNano())
			if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY,v VARCHAR(100),n DECIMAL(30,10))"); err != nil {
				failOperation(t, "create prepare table", err)
			}
			t.Cleanup(func() {
				if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
					failOperation(t, "drop prepare table", err)
				}
			})
			insert, err := db.PrepareContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?)")
			if err != nil {
				failOperation(t, "prepare insert", err)
			}
			for i, value := range []any{"hello", nil, "changed"} {
				result, err := insert.ExecContext(ctx, i+1, value, "12345678901234567890.1234567890")
				if err != nil {
					failOperation(t, "prepared insert", err)
				}
				if n, err := result.RowsAffected(); err != nil || n != 1 {
					t.Fatal(n, err)
				}
			}
			if err = insert.Close(); err != nil {
				failOperation(t, "close insert", err)
			}
			read, err := db.PrepareContext(ctx, "SELECT v,n FROM "+table+" WHERE id=?")
			if err != nil {
				failOperation(t, "prepare select", err)
			}
			defer func() {
				if err := read.Close(); err != nil {
					t.Error(err)
				}
			}()
			var text, binary sql.NullString
			var textNumber, binaryNumber string
			if err = db.QueryRowContext(ctx, "SELECT v,n FROM "+table+" WHERE id=1").Scan(&text, &textNumber); err != nil {
				failOperation(t, "text select", err)
			}
			if err = read.QueryRowContext(ctx, 1).Scan(&binary, &binaryNumber); err != nil {
				failOperation(t, "binary select", err)
			}
			exact, ok := new(big.Rat).SetString(binaryNumber)
			expected, _ := new(big.Rat).SetString("12345678901234567890.1234567890")
			if text != binary || textNumber != binaryNumber || !ok || exact.Cmp(expected) != 0 {
				t.Fatal("text/binary values differ")
			}
			if err = read.QueryRowContext(ctx, 2).Scan(&binary, &binaryNumber); err != nil || binary.Valid {
				t.Fatal(binary, err)
			}
			query := "SELECT ?"
			args := []any{int64(42)}
			if p.mode == oceanbase.ModeOracle {
				query = "SELECT :x,:X FROM dual"
				args = []any{sql.Named("x", int64(42))}
				var a, b int
				if err = db.QueryRowContext(ctx, query, args...).Scan(&a, &b); err != nil || a != 42 || b != 42 {
					t.Fatal(a, b, err)
				}
			} else {
				var a int
				if err = db.QueryRowContext(ctx, query, args...).Scan(&a); err != nil || a != 42 {
					t.Fatal(a, err)
				}
			}
		})
	}
}
