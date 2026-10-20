//go:build integration

package statement_test

import (
	bytes "bytes"
	sha256 "crypto/sha256"
	sql "database/sql"
	fmt "fmt"
	io "io"
	strings "strings"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func integrationTestLongParametersProfile(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.Limits.MaxLOBBytes = 1 << 20
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid long parameter configuration")
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	table := fmt.Sprintf("OBGLD_%d", time.Now().UnixNano())
	binaryType, textType, suffix := integrationIntegrationLongBlobType, "LONGTEXT", integrationIntegrationInnoDBSuffix
	if p.Mode == oceanbase.ModeOracle {
		binaryType, textType, suffix = integrationIntegrationBlobType, "CLOB", ""
	}
	if _, err = db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,B "+binaryType+",C "+textType+")"+suffix); err != nil {
		integrationtest.FailOperation(t, "create long parameter table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop long parameter table", err)
		}
	}()
	data := bytes.Repeat([]byte{0, 1, 127, 128, 255}, 450000)
	text := strings.Repeat("长参数🙂", 3000)
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(integrationtest.TestContext(t), nil)
		if err != nil {
			integrationtest.FailOperation(t, "begin long parameter transaction", err)
		}
		func() {
			defer func() { _ = tx.Rollback() }()
			input := &oceanbase.LongData{Reader: bytes.NewReader(data), Length: int64(len(data)), MaxBytes: 3 << 20}
			chars := &oceanbase.LongData{Reader: strings.NewReader(text), Length: -1, Text: true}
			if _, err := tx.ExecContext(integrationtest.TestContext(t), "INSERT INTO "+table+" VALUES (1,?,?)", input, chars); err != nil {
				integrationtest.FailOperation(t, "upload long parameters", err)
			}
			blob, err := oceanbase.OpenTableBlob(integrationtest.TestContext(t), tx, oceanbase.BlobTarget{Table: table, Column: "B", Key: map[string]any{"ID": 1}})
			if err != nil {
				integrationtest.FailOperation(t, "open uploaded BLOB", err)
			}
			hash := sha256.New()
			n, err := io.Copy(hash, blob.Reader(integrationtest.TestContext(t)))
			if err != nil {
				integrationtest.FailOperation(t, "hash uploaded BLOB", err)
			}
			want := sha256.Sum256(data)
			if n != int64(len(data)) || !bytes.Equal(hash.Sum(nil), want[:]) {
				t.Fatal("uploaded binary content hash differs")
			}
			if err = blob.Close(); err != nil {
				integrationtest.FailOperation(t, "close uploaded BLOB", err)
			}
			var got string
			if err = tx.QueryRowContext(integrationtest.TestContext(t), "SELECT C FROM "+table+" WHERE ID=1").Scan(&got); err != nil {
				integrationtest.FailOperation(t, "read uploaded text", err)
			}
			if sha256.Sum256([]byte(got)) != sha256.Sum256([]byte(text)) {
				t.Fatal("uploaded text content hash differs")
			}
			if commit {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				integrationtest.FailOperation(t, "finish long parameter transaction", err)
			}
		}()
		var count int
		if err = db.QueryRowContext(integrationtest.TestContext(t), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			integrationtest.FailOperation(t, "verify long parameter transaction", err)
		}
		if count != map[bool]int{false: 0, true: 1}[commit] {
			t.Fatal("long parameter transaction did not preserve commit/rollback")
		}
	}
}

const integrationIntegrationBlobType = "BLOB"

const integrationIntegrationInnoDBSuffix = " ENGINE=InnoDB"

const integrationIntegrationLongBlobType = "LONGBLOB"

func integrationTestExecuteStrategy(t *testing.T, p integrationtest.Profile, optimized bool) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.PrepareMode = oceanbase.PrepareServer
	cfg.Execution = oceanbase.ExecutionOptions{}
	if optimized {
		cfg.Execution = oceanbase.ExecutionOptions{PrepareExecute: true, StatementCacheSize: 4, ReuseParameterTypes: true, StatementChecksum: true}
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid strategy config")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer func() {
		if err := db.Close(); err != nil {
			t.Error("close optimized connection", err)
		}
	}()
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBGEXEC_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(100))"); err != nil {
		integrationtest.FailOperation(t, "create execution table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop execution table", err)
		}
	}()
	insert := "INSERT INTO " + table + " VALUES (?,?)"
	for i, value := range []any{"中🙂", nil, "strategy-updated"} {
		res, err := db.ExecContext(ctx, insert, i, value)
		if err != nil {
			integrationtest.FailOperation(t, "strategy insert", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Fatal("strategy affected rows", n, err)
		}
	}
	if _, err = db.ExecContext(ctx, insert, 0, "duplicate"); !integrationtest.DuplicateKey(err, p.Mode) {
		t.Fatal("strategy duplicate key error code mismatch")
	}
	for i, want := range []sql.NullString{{String: "中🙂", Valid: true}, {}, {String: "strategy-updated", Valid: true}} {
		var got sql.NullString
		if err = db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=?", i).Scan(&got); err != nil {
			integrationtest.FailOperation(t, "strategy read", err)
		}
		if got != want {
			t.Fatal("strategy changed result", got, want)
		}
	}
	var count int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 3 {
		t.Fatal("strategy replayed business input", count, err)
	}
	integrationTestStrategyOutput(t, p, db)
}

func integrationTestStrategyOutput(t *testing.T, p integrationtest.Profile, db *sql.DB) {
	t.Helper()
	query := "BEGIN ? := 42; END;"
	if p.Mode == oceanbase.ModeMySQL {
		procedure := fmt.Sprintf("OBGEXECP_%d", time.Now().UnixNano())
		if _, err := db.ExecContext(integrationtest.TestContext(t), "CREATE PROCEDURE "+procedure+"(OUT y BIGINT) SET y=42"); err != nil {
			integrationtest.FailOperation(t, "create strategy procedure", err)
		}
		defer func() {
			if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP PROCEDURE "+procedure); err != nil {
				integrationtest.FailOperation(t, "drop strategy procedure", err)
			}
		}()
		query = "CALL " + procedure + "(?)"
	}
	for range 2 {
		var got int64
		if _, err := db.ExecContext(integrationtest.TestContext(t), query, sql.Out{Dest: &got}); err != nil {
			integrationtest.FailOperation(t, "strategy output", err)
		}
		if got != 42 {
			t.Fatal("strategy changed output")
		}
	}
}
