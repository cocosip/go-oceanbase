package main

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"

	"github.com/cocosip/go-oceanbase"
)

type floatInput struct {
	id    int
	value any
	label string
	want  sql.NullFloat64
}

// floatInputs covers the boundary set that also distinguishes exact storage from
// text round trips: fractions, both extremes, the smallest subnormal, negative
// zero, NULL and a float32 that widens to a non-obvious float64.
func floatInputs() []floatInput {
	maxValue := math.MaxFloat64
	small := math.SmallestNonzeroFloat64
	zero := math.Copysign(0, -1)
	widened := float64(float32(1.1))
	return []floatInput{
		{1, 1.5, "after-float-1", sql.NullFloat64{Float64: 1.5, Valid: true}},
		{2, 1.1, "after-float-2", sql.NullFloat64{Float64: 1.1, Valid: true}},
		{3, maxValue, "after-float-3", sql.NullFloat64{Float64: maxValue, Valid: true}},
		{4, -maxValue, "after-float-4", sql.NullFloat64{Float64: -maxValue, Valid: true}},
		{5, small, "after-float-5", sql.NullFloat64{Float64: small, Valid: true}},
		{6, zero, "after-float-6", sql.NullFloat64{Float64: zero, Valid: true}},
		{7, nil, "after-float-7", sql.NullFloat64{}},
		{8, float32(1.1), "after-float-8", sql.NullFloat64{Float64: widened, Valid: true}},
	}
}

// runFloats verifies MySQL DOUBLE / Oracle BINARY_DOUBLE bit-exact round trips.
// Oracle may normalize negative zero, so that row is compared against a literal
// control instead of demanding the sign survive storage.
func runFloats(ctx context.Context, s *scenario) error {
	column := "DOUBLE"
	oracle := s.mode == oceanbase.ModeOracle
	if oracle {
		column = "BINARY_DOUBLE"
	}
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V "+column+",LABEL VARCHAR(80))")
	if err != nil {
		return err
	}
	inputs := floatInputs()
	for _, input := range inputs {
		if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?)", input.id, input.value, input.label); err != nil {
			return err
		}
	}
	for _, input := range inputs {
		var got sql.NullFloat64
		var label string
		if err = s.db.QueryRowContext(ctx, "SELECT V,LABEL FROM "+table+" WHERE ID=?", input.id).Scan(&got, &label); err != nil {
			return err
		}
		if err = checkFloatValue(got, input.want, oracle); err != nil {
			return err
		}
		if err = expect(label == input.label, "float label parameter changed"); err != nil {
			return err
		}
	}
	want := len(inputs)
	if oracle {
		if err = checkOracleNegativeZero(ctx, s.db, table); err != nil {
			return err
		}
		if err = checkOracleStoredText(ctx, s.db, table, inputs); err != nil {
			return err
		}
		want++
	}
	var total int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&total); err != nil {
		return err
	}
	return expect(total == want, "float row count changed")
}

func checkFloatValue(got, want sql.NullFloat64, oracle bool) error {
	if got.Valid != want.Valid {
		return checkFailure("float NULL validity changed")
	}
	if !want.Valid {
		return nil
	}
	if oracle && want.Float64 == 0 && math.Signbit(want.Float64) {
		return expect(got.Float64 == 0, "oracle negative zero became nonzero")
	}
	return expect(math.Float64bits(got.Float64) == math.Float64bits(want.Float64), "float value changed")
}

func checkOracleNegativeZero(ctx context.Context, db *sql.DB, table string) error {
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (9,TO_BINARY_DOUBLE('-0'),'literal-zero')"); err != nil {
		return err
	}
	var bound, literal string
	if err := db.QueryRowContext(ctx, "SELECT DUMP(V) FROM "+table+" WHERE ID=?", 6).Scan(&bound); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "SELECT DUMP(V) FROM "+table+" WHERE ID=?", 9).Scan(&literal); err != nil {
		return err
	}
	return expect(bound == literal, "oracle negative zero storage differs from the literal control")
}

func checkOracleStoredText(ctx context.Context, db *sql.DB, table string, inputs []floatInput) error {
	for _, input := range inputs[:2] {
		var text string
		if err := db.QueryRowContext(ctx, "SELECT TO_CHAR(V) FROM "+table+" WHERE ID=?", input.id).Scan(&text); err != nil {
			return err
		}
		stored, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return checkFailure("oracle stored float text is not numeric")
		}
		if math.Float64bits(stored) != math.Float64bits(input.want.Float64) {
			return checkFailure(fmt.Sprintf("oracle stored float text changed for row %d", input.id))
		}
	}
	return nil
}
