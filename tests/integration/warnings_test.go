//go:build integration

package integration

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

func TestWarningDetails(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testWarningDetails(t, p) })
	}
}

func testWarningDetails(t *testing.T, p profile) {
	t.Helper()
	key := strings.TrimSuffix(p.env, "_DSN") + "_WARNING_SQL"
	query := os.Getenv(key)
	if query == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required warning fixture missing: " + p.name)
		}
		t.Skip("warning fixture not configured: " + p.name)
	}
	db := openProfile(t, p)
	ctx := testContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin warning session", err)
	}
	defer func() { _ = pinned.Close() }()
	// The deployment supplies a successful statement that produces nonzero warnings.
	if _, err = pinned.ExecContext(ctx, query); err != nil {
		failOperation(t, "execute warning fixture", err)
	}
	count, err := oceanbase.Warnings(ctx, pinned)
	if err != nil || !count.Known || count.Count == 0 {
		t.Fatal("fixture did not produce a protocol warning count")
	}
	details, err := oceanbase.ReadWarnings(ctx, pinned)
	if p.mode == oceanbase.ModeOracle {
		if !errors.Is(err, oceanbase.ErrUnsupported) {
			t.Fatal("Oracle warning detail boundary was not explicit")
		}
	} else {
		if err != nil {
			failOperation(t, "read warning details", err)
		}
		if len(details) == 0 {
			t.Fatal("MySQL fixture must retain server warning details")
		}
		for _, w := range details {
			if w.Level == "" || w.Code == 0 || w.Message == "" {
				t.Fatal("incomplete server warning details")
			}
		}
	}
	after, err := oceanbase.Warnings(ctx, pinned)
	if err != nil || after != count {
		t.Fatal("reading details replaced the ordinary warning count")
	}
	if err = oceanbase.ClearWarnings(ctx, pinned); err != nil {
		failOperation(t, "clear warning snapshot", err)
	}
	after, err = oceanbase.Warnings(ctx, pinned)
	if err != nil || after != (oceanbase.WarningSummary{Known: true}) {
		t.Fatal("local warning clear failed")
	}
	if p.mode == oceanbase.ModeMySQL {
		if details, err = oceanbase.ReadWarnings(ctx, pinned); err != nil || len(details) != 0 {
			t.Fatal("cleared warning details reappeared")
		}
	}
}
