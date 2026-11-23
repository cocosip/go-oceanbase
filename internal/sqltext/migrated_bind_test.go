package sqltext

import (
	"errors"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"testing"
)

func TestBindAdjacentHashComment(t *testing.T) {
	for _, query := range []string{"SELECT 1# ?\n", "SELECT 1# ; SELECT 2\n"} {
		p, err := BuildBindPlan(query, config.CompatibilityModeMySQL)
		if err != nil || p.NumInput != 0 {
			t.Fatalf("comment interpreted as SQL: %#v %v", p, err)
		}
	}
}

func TestBindEscapeModes(t *testing.T) {
	query := "SELECT 'a\\', ?"
	if _, err := BuildBindPlanEscapes(query, config.CompatibilityModeMySQL, false); err == nil {
		t.Fatal("backslash escaping ignored")
	}
	for _, mode := range []config.CompatibilityMode{config.CompatibilityModeMySQL, config.CompatibilityModeOracle} {
		p, err := BuildBindPlanEscapes(query, mode, true)
		if err != nil || p.NumInput != 1 {
			t.Fatal(p, err)
		}
	}
	p, err := BuildBindPlan("SELECT 1--2, ?", config.CompatibilityModeMySQL)
	if err != nil || p.NumInput != 1 {
		t.Fatal(p, err)
	}
	p, err = BuildBindPlan("SELECT nq'¡:hidden ?¡', :x FROM dual", config.CompatibilityModeOracle)
	if err != nil || p.NumInput != 1 {
		t.Fatal(p, err)
	}
}

func TestBindAmbiguousQuotes(t *testing.T) {
	for _, query := range []string{`SELECT 1 AS "a\", ?`, `SELECT "a\" ? ", ?`} {
		if _, err := BuildBindPlan(query, config.CompatibilityModeMySQL); !errors.Is(err, dberrors.ErrBind) {
			t.Fatalf("ambiguous interpolation spans accepted: %q %v", query, err)
		}
	}
}
