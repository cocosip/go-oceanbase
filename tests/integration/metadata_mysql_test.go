//go:build integration

package integration

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestMySQLMetadataRegressions(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.name, func(t *testing.T) {
			t.Run("temporal-scale", func(t *testing.T) { testMySQLTemporalMetadata(t, p) })
			for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				t.Run(fmt.Sprint(prepare), func(t *testing.T) { testMySQLNamedCall(t, p, prepare) })
			}
		})
	}
}

func testMySQLTemporalMetadata(t *testing.T, p profile) {
	t.Helper()
	db := openProfile(t, p)
	ctx := testContext(t)
	table := fmt.Sprintf("OBGTEMP_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (tm TIME(3),dt DATETIME(6),ts TIMESTAMP(2),yr YEAR(4))"); err != nil {
		failOperation(t, "create temporal table", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop temporal table", err)
		}
	}()
	c, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin temporal metadata", err)
	}
	defer func() { _ = c.Close() }()
	var schema string
	if err = c.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&schema); err != nil {
		failOperation(t, "read temporal schema", err)
	}
	rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{Kind: oceanbase.MetadataColumns, SchemaPattern: metadataPattern(schema), NamePattern: metadataPattern(table)})
	if err != nil {
		failOperation(t, "temporal metadata", err)
	}
	defer func() { _ = rows.Close() }()
	for i, want := range []int64{3, 6, 2, 0} {
		if !rows.Next() {
			t.Fatal("temporal metadata row missing", i, rows.Err())
		}
		values, targets := make([]any, 15), make([]any, 15)
		for j := range targets {
			targets[j] = &values[j]
		}
		var scale sql.NullInt64
		targets[8] = &scale
		if err = rows.Scan(targets...); err != nil {
			failOperation(t, "scan temporal metadata", err)
		}
		if scale.Valid != (i != 3) || scale.Int64 != want {
			t.Fatalf("column %d temporal scale=%v want=%d", i, scale, want)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("temporal metadata row boundary", rows.Err())
	}
}

func testMySQLNamedCall(t *testing.T, p profile, prepare oceanbase.PrepareMode) {
	t.Helper()
	cfg := profileConfig(t, p)
	cfg.PrepareMode = prepare
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		failOperation(t, "named call connector", err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx := testContext(t)
	c, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin named call", err)
	}
	defer func() { _ = c.Close() }()
	name := fmt.Sprintf("OBGNAMED_%d", time.Now().UnixNano())
	if _, err = c.ExecContext(ctx, "CREATE FUNCTION "+name+"(different BIGINT) RETURNS BIGINT DETERMINISTIC RETURN different*2"); err != nil {
		failOperation(t, "create same-name function", err)
	}
	defer func() {
		if _, err := c.ExecContext(testContext(t), "DROP FUNCTION "+name); err != nil {
			failOperation(t, "drop named function", err)
		}
	}()
	if _, err = c.ExecContext(ctx, "CREATE PROCEDURE "+name+"(IN yr YEAR(4),OUT value BIGINT) SET value=yr"); err != nil {
		failOperation(t, "create same-name procedure", err)
	}
	defer func() {
		if _, err := c.ExecContext(testContext(t), "DROP PROCEDURE IF EXISTS "+name); err != nil {
			failOperation(t, "drop named procedure", err)
		}
	}()
	var value int64
	if _, err = c.ExecContext(ctx, "CALL "+name+"(?,?)", sql.Named("value", sql.Out{Dest: &value}), sql.Named("YR", 2026)); err != nil {
		failOperation(t, "named YEAR call", err)
	}
	if value != 2026 {
		t.Fatal("named procedure/function namespace or argument order differs", value)
	}
	if _, err = c.ExecContext(ctx, "DROP PROCEDURE "+name); err != nil {
		failOperation(t, "remove named procedure", err)
	}
	_, err = c.ExecContext(ctx, "CALL "+name+"(?)", sql.Named("different", 1))
	var server *oceanbase.Error
	if !errors.Is(err, oceanbase.ErrBind) && (!errors.As(err, &server) || server.Code != 1305) {
		t.Fatal("missing procedure resolved through same-name function", err)
	}
	if err = c.PingContext(ctx); err != nil {
		failOperation(t, "reuse after missing procedure", err)
	}
}
