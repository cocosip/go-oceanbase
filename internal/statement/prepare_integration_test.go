//go:build integration

package statement_test

import (
	sql "database/sql"
	fmt "fmt"
	big "math/big"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestPrepareRoundTrip(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			db := integrationtest.OpenProfile(t, p)
			ctx := integrationtest.TestContext(t)
			table := fmt.Sprintf("obgo_ps_%d", time.Now().UnixNano())
			if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY,v VARCHAR(100),n DECIMAL(30,10))"); err != nil {
				integrationtest.FailOperation(t, "create prepare table", err)
			}
			t.Cleanup(func() {
				if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
					integrationtest.FailOperation(t, "drop prepare table", err)
				}
			})
			insert, err := db.PrepareContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?)")
			if err != nil {
				integrationtest.FailOperation(t, "prepare insert", err)
			}
			for i, value := range []any{"hello", nil, "changed"} {
				result, err := insert.ExecContext(ctx, i+1, value, "12345678901234567890.1234567890")
				if err != nil {
					integrationtest.FailOperation(t, "prepared insert", err)
				}
				if n, err := result.RowsAffected(); err != nil || n != 1 {
					t.Fatal(n, err)
				}
			}
			if err = insert.Close(); err != nil {
				integrationtest.FailOperation(t, "close insert", err)
			}
			read, err := db.PrepareContext(ctx, "SELECT v,n FROM "+table+" WHERE id=?")
			if err != nil {
				integrationtest.FailOperation(t, "prepare select", err)
			}
			defer func() {
				if err := read.Close(); err != nil {
					t.Error(err)
				}
			}()
			var text, binary sql.NullString
			var textNumber, binaryNumber string
			if err = db.QueryRowContext(ctx, "SELECT v,n FROM "+table+" WHERE id=1").Scan(&text, &textNumber); err != nil {
				integrationtest.FailOperation(t, "text select", err)
			}
			if err = read.QueryRowContext(ctx, 1).Scan(&binary, &binaryNumber); err != nil {
				integrationtest.FailOperation(t, "binary select", err)
			}
			exact, ok := new(big.Rat).SetString(binaryNumber)
			expected, _ := new(big.Rat).SetString("12345678901234567890.1234567890")
			if text != binary || textNumber != binaryNumber || !ok || exact.Cmp(expected) != 0 {
				t.Fatal("text/binary values differ")
			}
			if err = read.QueryRowContext(ctx, 2).Scan(&binary, &binaryNumber); err != nil || binary.Valid {
				t.Fatal(binary, err)
			}
			query := integrationtest.SelectParameterSQL
			args := []any{int64(42)}
			if p.Mode == oceanbase.ModeOracle {
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
