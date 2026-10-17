//go:build integration

package integration

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestLongParameters(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testLongParametersProfile(t, p) })
	}
}

func testLongParametersProfile(t *testing.T, p profile) {
	t.Helper()
	cfg := profileConfig(t, p)
	cfg.Limits.MaxLOBBytes = 1 << 20
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid long parameter configuration")
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	table := fmt.Sprintf("OBGLD_%d", time.Now().UnixNano())
	binaryType, textType, suffix := integrationLongBlobType, "LONGTEXT", integrationInnoDBSuffix
	if p.mode == oceanbase.ModeOracle {
		binaryType, textType, suffix = integrationBlobType, "CLOB", ""
	}
	if _, err = db.ExecContext(testContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,B "+binaryType+",C "+textType+")"+suffix); err != nil {
		failOperation(t, "create long parameter table", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop long parameter table", err)
		}
	}()
	data := bytes.Repeat([]byte{0, 1, 127, 128, 255}, 450000)
	text := strings.Repeat("长参数🙂", 3000)
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(testContext(t), nil)
		if err != nil {
			failOperation(t, "begin long parameter transaction", err)
		}
		func() {
			defer func() { _ = tx.Rollback() }()
			input := &oceanbase.LongData{Reader: bytes.NewReader(data), Length: int64(len(data)), MaxBytes: 3 << 20}
			chars := &oceanbase.LongData{Reader: strings.NewReader(text), Length: -1, Text: true}
			if _, err := tx.ExecContext(testContext(t), "INSERT INTO "+table+" VALUES (1,?,?)", input, chars); err != nil {
				failOperation(t, "upload long parameters", err)
			}
			blob, err := oceanbase.OpenTableBlob(testContext(t), tx, oceanbase.BlobTarget{Table: table, Column: "B", Key: map[string]any{"ID": 1}})
			if err != nil {
				failOperation(t, "open uploaded BLOB", err)
			}
			hash := sha256.New()
			n, err := io.Copy(hash, blob.Reader(testContext(t)))
			if err != nil {
				failOperation(t, "hash uploaded BLOB", err)
			}
			want := sha256.Sum256(data)
			if n != int64(len(data)) || !bytes.Equal(hash.Sum(nil), want[:]) {
				t.Fatal("uploaded binary content hash differs")
			}
			if err = blob.Close(); err != nil {
				failOperation(t, "close uploaded BLOB", err)
			}
			var got string
			if err = tx.QueryRowContext(testContext(t), "SELECT C FROM "+table+" WHERE ID=1").Scan(&got); err != nil {
				failOperation(t, "read uploaded text", err)
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
				failOperation(t, "finish long parameter transaction", err)
			}
		}()
		var count int
		if err = db.QueryRowContext(testContext(t), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			failOperation(t, "verify long parameter transaction", err)
		}
		if count != map[bool]int{false: 0, true: 1}[commit] {
			t.Fatal("long parameter transaction did not preserve commit/rollback")
		}
	}
}
