//go:build integration

package valuecodec_test

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"strconv"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

const (
	binaryDoubleArrayPath            = "array"
	binaryDoubleCombinedPreparedPath = "combined-prepared"
)

func TestBinaryDoubleRange(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		t.Run(profile.Name, func(t *testing.T) {
			for _, path := range []string{"ordinary", "prepared", "combined", binaryDoubleCombinedPreparedPath, binaryDoubleArrayPath} {
				t.Run(path, func(t *testing.T) { testBinaryDoubleRange(t, profile, path) })
			}
		})
	}
}

func testBinaryDoubleRange(t *testing.T, profile integrationtest.Profile, path string) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, profile)
	cfg.PrepareMode = oceanbase.PrepareServer
	cfg.Execution.PrepareExecute = path == "combined" || path == binaryDoubleCombinedPreparedPath
	cfg.Execution.ReuseParameterTypes = true
	cfg.Execution.StatementCacheSize = 4
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBGD_%d", time.Now().UnixNano())
	column := "DOUBLE"
	if profile.Mode == oceanbase.ModeOracle {
		column = "BINARY_DOUBLE"
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V "+column+",LABEL VARCHAR(80))"); err != nil {
		integrationtest.FailOperation(t, "create double table", err)
	}
	t.Logf("owned test table=%s", table)
	defer func() {
		// A malformed execution can poison its connection; clean up on a fresh session.
		cleanupDB := sql.OpenDB(connector)
		defer func() { _ = cleanupDB.Close() }()
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := cleanupDB.ExecContext(cleanup, "DROP TABLE "+table); err != nil {
			t.Errorf("drop %s: %v", table, err)
		}
	}()
	query := "INSERT INTO " + table + " (ID,V,LABEL) VALUES (?,?,?)"
	maxFloat := math.MaxFloat64
	inputs := []any{1.5, 1.1, maxFloat, 1e200, -maxFloat, math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64, math.Copysign(0, -1), &maxFloat, (*float64)(nil), sql.NullFloat64{}, sql.NullFloat64{Float64: -maxFloat, Valid: true}, float32(1.1), maxFloat}
	if path == binaryDoubleArrayPath {
		// Pointer and Valuer inputs intentionally select sequential fallback in ExecBatch.
		inputs = []any{1.5, 1.1, maxFloat, 1e200, -maxFloat, math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64, math.Copysign(0, -1), nil, float32(1.1), maxFloat}
	}
	rows := make([][]any, len(inputs))
	for i, input := range inputs {
		rows[i] = []any{i, input, fmt.Sprintf("after-float-%d", i)}
	}
	if path == binaryDoubleArrayPath {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		result, err := oceanbase.ExecBatch(ctx, tx, query, rows, oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding, ContinueOnError: true, ChunkRows: len(rows)})
		if err != nil {
			integrationtest.FailOperation(t, "batch double insert", err)
		}
		want := oceanbase.BatchSequential
		if profile.Mode == oceanbase.ModeOracle {
			want = oceanbase.BatchArrayBinding
		}
		if result.Strategy != want {
			t.Fatalf("batch strategy=%d want=%d", result.Strategy, want)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	} else {
		exec := func(args ...any) (sql.Result, error) { return db.ExecContext(ctx, query, args...) }
		if path == "prepared" || path == binaryDoubleCombinedPreparedPath {
			stmt, err := db.PrepareContext(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stmt.Close() }()
			exec = func(args ...any) (sql.Result, error) { return stmt.ExecContext(ctx, args...) }
		}
		for _, row := range rows {
			if _, err := exec(row...); err != nil {
				integrationtest.FailOperation(t, "insert double", err)
			}
		}
	}
	testBinaryDoubleReadback(ctx, t, db, profile.Mode, table, inputs)
}

func testBinaryDoubleReadback(ctx context.Context, t *testing.T, db *sql.DB, mode oceanbase.Mode, table string, inputs []any) {
	t.Helper()
	var zeroControl string
	if mode == oceanbase.ModeOracle {
		var boundZero string
		if err := db.QueryRowContext(ctx, "SELECT DUMP(?) FROM dual", math.Copysign(0, -1)).Scan(&boundZero); err != nil {
			integrationtest.FailOperation(t, "inspect bound negative zero", err)
		}
		if boundZero != "Typ=12 Len=8: 0,0,0,0,0,0,0,128" {
			t.Errorf("negative zero parameter lost sign: %s", boundZero)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (999,TO_BINARY_DOUBLE('-0'),'literal control')"); err != nil {
			integrationtest.FailOperation(t, "insert literal negative zero control", err)
		}
		if err := db.QueryRowContext(ctx, "SELECT DUMP(V) FROM "+table+" WHERE ID=999").Scan(&zeroControl); err != nil {
			integrationtest.FailOperation(t, "read literal negative zero control", err)
		}
		t.Logf("negative zero: bound=%s literal stored=%s", boundZero, zeroControl)
	}
	for i, input := range inputs {
		var want sql.NullFloat64
		switch v := input.(type) {
		case float64:
			want = sql.NullFloat64{Float64: v, Valid: true}
		case float32:
			want = sql.NullFloat64{Float64: float64(v), Valid: true}
		case *float64:
			if v != nil {
				want = sql.NullFloat64{Float64: *v, Valid: true}
			}
		case sql.NullFloat64:
			want = v
		}
		var got sql.NullFloat64
		var label string
		if err := db.QueryRowContext(ctx, "SELECT V,LABEL FROM "+table+" WHERE ID=?", i).Scan(&got, &label); err != nil {
			integrationtest.FailOperation(t, "read double", err)
		}
		oracleNegativeZero := mode == oceanbase.ModeOracle && want.Valid && want.Float64 == 0 && math.Signbit(want.Float64)
		if oracleNegativeZero {
			// Compare storage with a literal control: Oracle may normalize negative zero.
			var dump string
			if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT DUMP(V) FROM %s WHERE ID=%d", table, i)).Scan(&dump); err != nil {
				integrationtest.FailOperation(t, "read stored negative zero", err)
			}
			if dump != zeroControl {
				t.Errorf("stored negative zero=%s literal control=%s", dump, zeroControl)
			}
		}
		if got.Valid != want.Valid || got.Valid && (got.Float64 != want.Float64 || !oracleNegativeZero && math.Float64bits(got.Float64) != math.Float64bits(want.Float64)) {
			t.Errorf("row=%d got=%+v bits=%016x want=%+v bits=%016x", i, got, math.Float64bits(got.Float64), want, math.Float64bits(want.Float64))
		}
		if label != fmt.Sprintf("after-float-%d", i) {
			t.Errorf("row=%d following parameter=%q", i, label)
		}
		if mode == oceanbase.ModeOracle && i < 4 {
			var text string
			if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT TO_CHAR(V) FROM %s WHERE ID=%d", table, i)).Scan(&text); err != nil {
				integrationtest.FailOperation(t, "independent stored text", err)
			}
			t.Logf("input=%v server TO_CHAR(V)=%s", input, text)
			stored, err := strconv.ParseFloat(text, 64)
			if err != nil || math.Float64bits(stored) != math.Float64bits(want.Float64) {
				t.Errorf("server stored text=%q want=%v: %v", text, want.Float64, err)
			}
		}
	}
}
