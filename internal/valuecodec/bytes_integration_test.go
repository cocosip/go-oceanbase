//go:build integration

package valuecodec_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestOracleBytesBlobRoundTrip(t *testing.T) {
	profile := integrationtest.Profile{Name: "oracle", Env: "OB_ORACLE_DSN", Mode: oceanbase.ModeOracle}
	for _, combined := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("combined=%t/prepared=%t", combined, explicit), func(t *testing.T) {
				cfg := integrationtest.ProfileConfig(t, profile)
				cfg.Execution.PrepareExecute, cfg.Execution.ReuseParameterTypes = combined, true
				cfg.Limits.MaxLOBBytes = 150000
				connector, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				db.SetMaxOpenConns(1)
				defer func() { _ = db.Close() }()
				ctx := integrationtest.TestContext(t)
				table := fmt.Sprintf("OBGB_%d", time.Now().UnixNano())
				if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID NUMBER PRIMARY KEY,V BLOB)"); err != nil {
					integrationtest.FailOperation(t, "create byte table", err)
				}
				defer func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, err := db.ExecContext(cleanup, "DROP TABLE "+table); err != nil {
						t.Error(err)
					}
				}()
				query := "INSERT INTO " + table + " (ID,V) VALUES (?,?)"
				exec := func(args ...any) (sql.Result, error) { return db.ExecContext(ctx, query, args...) }
				if explicit {
					stmt, err := db.PrepareContext(ctx, query)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = stmt.Close() }()
					exec = func(args ...any) (sql.Result, error) { return stmt.ExecContext(ctx, args...) }
				}
				large := bytes.Repeat([]byte{0, 1, 127, 128, 255}, 30000)
				for i, want := range [][]byte{nil, {}, {0, 1, 127, 255}, large, {255, 0}, nil} {
					if _, err := exec(i, want); err != nil {
						integrationtest.FailOperation(t, "insert bytes", err)
					}
					var got []byte
					var null int
					if err := db.QueryRowContext(ctx, "SELECT V,CASE WHEN V IS NULL THEN 1 ELSE 0 END FROM "+table+" WHERE ID=?", i).Scan(&got, &null); err != nil {
						t.Fatal(err)
					}
					// Oracle's empty VARCHAR binding is NULL, matching Connector/J setBytes.
					if !bytes.Equal(got, want) || (null == 1) != (len(want) == 0) {
						t.Fatalf("row=%d bytes=%d NULL=%d", i, len(got), null)
					}
				}
				if _, err := exec(100, append(large, 1)); !errors.Is(err, wire.ErrMessageTooLarge) {
					t.Fatalf("LOB byte limit: %v", err)
				}
				if err := db.PingContext(ctx); err != nil {
					t.Fatal(err)
				}
				var count int
				if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 6 {
					t.Fatalf("count=%d error=%v", count, err)
				}
			})
		}
	}
}
