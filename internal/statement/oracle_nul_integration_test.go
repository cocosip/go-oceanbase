//go:build integration

package statement_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestOracleClientLongNUL(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeOracle {
			continue
		}
		for _, length := range []int{12003, 98304, 196608, 1048576} {
			for _, count := range []int{1, 2} {
				t.Run(fmt.Sprintf("%s/bytes=%d/selects=%d", profile.Name, length, count), func(t *testing.T) {
					cfg := integrationtest.ProfileConfig(t, profile)
					cfg.PrepareMode, cfg.AllowMultiStatements = oceanbase.PrepareClient, true
					connector, err := oceanbase.NewConnector(cfg)
					if err != nil {
						t.Fatal(err)
					}
					db := sql.OpenDB(connector)
					defer func() { _ = db.Close() }()
					want := strings.Repeat("a", length/2) + "\x00" + strings.Repeat("b", length-length/2-1)
					query := strings.TrimSuffix(strings.Repeat("SELECT :v FROM DUAL;", count), ";")
					ctx, cancel := context.WithTimeout(integrationtest.TestContext(t), 10*time.Second)
					defer cancel()
					c, err := db.Conn(ctx)
					if err != nil {
						integrationtest.FailOperation(t, "pin long NUL connection", err)
					}
					defer func() { _ = c.Close() }()
					rows, err := c.QueryContext(ctx, query, sql.Named("v", want))
					if err != nil {
						integrationtest.FailOperation(t, "execute long NUL script", err)
					}
					defer func() { _ = rows.Close() }()
					checkOracleNULResults(t, rows, want, count)
					if err := c.PingContext(ctx); err != nil {
						integrationtest.FailOperation(t, "reuse long NUL connection", err)
					}
				})
			}
		}
	}
}

func checkOracleNULResults(t *testing.T, rows *sql.Rows, want string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if i > 0 && !rows.NextResultSet() {
			t.Fatal("missing NUL result set", rows.Err())
		}
		if !rows.Next() {
			t.Fatal("missing NUL row", rows.Err())
		}
		var got string
		if err := rows.Scan(&got); err != nil || got != want {
			t.Fatalf("NUL result differs: bytes=%d want=%d err=%v", len(got), len(want), err)
		}
		if rows.Next() || rows.Err() != nil {
			t.Fatal("unexpected NUL rows", rows.Err())
		}
	}
	if rows.NextResultSet() || rows.Err() != nil {
		t.Fatal("unexpected NUL results", rows.Err())
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOracleClientLongNULStorage(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeOracle {
			continue
		}
		t.Run(profile.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, profile)
			cfg.PrepareMode, cfg.AllowMultiStatements = oceanbase.PrepareClient, true
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			ctx := integrationtest.TestContext(t)
			table := fmt.Sprintf("OBGNU_%d", time.Now().UnixNano())
			if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V CLOB)"); err != nil {
				integrationtest.FailOperation(t, "create long NUL storage", err)
			}
			defer func() {
				if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
					integrationtest.FailOperation(t, "drop long NUL storage", err)
				}
			}()
			observer := integrationtest.OpenProfile(t, profile)
			for index, length := range []int{12003, 98304, 196608} {
				want := strings.Repeat("a", length/2) + "\x00" + strings.Repeat("b", length-length/2-1)
				plain := strings.ReplaceAll(want, "\x00", "x")
				first := index*2 + 1
				query := "INSERT INTO " + table + " VALUES (:a,:v); INSERT INTO " + table + " VALUES (:b,:w)"
				writeCtx, cancel := context.WithTimeout(integrationtest.TestContext(t), 10*time.Second)
				_, err := db.ExecContext(writeCtx, query, sql.Named("a", first), sql.Named("v", want), sql.Named("b", first+1), sql.Named("w", plain))
				cancel()
				if err != nil {
					integrationtest.FailOperation(t, "store NUL and control script", err)
				}
				for i, value := range []string{want, plain} {
					checkOracleNULStorage(t, observer, table, first+i, value)
				}
			}
		})
	}
}

func checkOracleNULStorage(t *testing.T, db *sql.DB, table string, id int, want string) {
	t.Helper()
	var length int
	if err := db.QueryRowContext(integrationtest.TestContext(t), "SELECT DBMS_LOB.GETLENGTH(V) FROM "+table+" WHERE ID=?", id).Scan(&length); err != nil || length != len(want) {
		t.Fatal("independent NUL storage length", length, err)
	}
	// Keep below DBMS_LOB.SUBSTR's character-set-dependent return limit.
	const chunkSize = 2000
	for offset := 0; offset < len(want); offset += chunkSize {
		var got string
		if err := db.QueryRowContext(integrationtest.TestContext(t), "SELECT DBMS_LOB.SUBSTR(V,2000,?) FROM "+table+" WHERE ID=?", offset+1, id).Scan(&got); err != nil {
			integrationtest.FailOperation(t, "read independent NUL storage chunk", err)
		}
		if got != want[offset:min(offset+chunkSize, len(want))] {
			mismatch := 0
			for mismatch < len(got) && offset+mismatch < len(want) && got[mismatch] == want[offset+mismatch] {
				mismatch++
			}
			t.Fatalf("independent NUL storage content: id=%d offset=%d got=%d want=%d firstDifference=%d", id, offset, len(got), min(chunkSize, len(want)-offset), mismatch)
		}
	}
}

// TestOracleClientLongNULStorageBoundary verifies the largest client literal the
// tested target accepts (exactly 1 MiB) without reading every 2000-byte chunk.
func TestOracleClientLongNULStorageBoundary(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeOracle {
			continue
		}
		t.Run(profile.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, profile)
			cfg.PrepareMode, cfg.AllowMultiStatements = oceanbase.PrepareClient, true
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			table := fmt.Sprintf("OBGNB_%d", time.Now().UnixNano())
			if _, err := db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V CLOB)"); err != nil {
				integrationtest.FailOperation(t, "create NUL boundary storage", err)
			}
			defer func() {
				if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
					integrationtest.FailOperation(t, "drop NUL boundary storage", err)
				}
			}()
			observer := integrationtest.OpenProfile(t, profile)
			const length = 1 << 20
			want := strings.Repeat("a", length/2) + "\x00" + strings.Repeat("b", length-length/2-1)
			writeCtx, cancel := context.WithTimeout(integrationtest.TestContext(t), 20*time.Second)
			_, err = db.ExecContext(writeCtx, "INSERT INTO "+table+" VALUES (:a,:v)", sql.Named("a", 1), sql.Named("v", want))
			cancel()
			if err != nil {
				integrationtest.FailOperation(t, "store 1 MiB NUL value", err)
			}
			var storedLength, nulPosition int
			if err := observer.QueryRowContext(integrationtest.TestContext(t), "SELECT DBMS_LOB.GETLENGTH(V),INSTR(V,CHR(0)) FROM "+table+" WHERE ID=1").Scan(&storedLength, &nulPosition); err != nil {
				integrationtest.FailOperation(t, "read 1 MiB NUL metadata", err)
			}
			if storedLength != len(want) || nulPosition != length/2+1 {
				t.Fatalf("1 MiB NUL shape changed: length=%d nul=%d", storedLength, nulPosition)
			}
			for name, query := range map[string]string{
				"prefix": "SELECT DBMS_LOB.SUBSTR(V,2000,1) FROM " + table + " WHERE ID=1",
				"suffix": "SELECT DBMS_LOB.SUBSTR(V,2000," + fmt.Sprint(length-1999) + ") FROM " + table + " WHERE ID=1",
			} {
				var got string
				if err := observer.QueryRowContext(integrationtest.TestContext(t), query).Scan(&got); err != nil {
					integrationtest.FailOperation(t, "read 1 MiB NUL "+name, err)
				}
				wantPart := want[:2000]
				if name == "suffix" {
					wantPart = want[len(want)-2000:]
				}
				if got != wantPart {
					t.Fatalf("1 MiB NUL %s changed", name)
				}
			}
		})
	}
}
