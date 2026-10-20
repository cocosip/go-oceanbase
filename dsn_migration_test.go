package oceanbase

import (
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func assertMigrationOptionRejected(t *testing.T, name string) {
	t.Helper()
	const sensitive = "migration-secret@tenant#&value"
	u := url.URL{
		Scheme: "oceanbase", Host: "localhost", Path: "/demo",
		User:     url.UserPassword(testUser, sensitive),
		RawQuery: url.Values{name: {sensitive}}.Encode(),
	}
	_, err := ParseDSN(u.String())
	assertMigrationConfigError(t, err, "DSN option", sensitive)
	db, err := sql.Open("oceanbase", u.String())
	if db != nil {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	assertMigrationConfigError(t, err, "DSN option", sensitive)
}

func assertMigrationConfigError(t *testing.T, err error, field, sensitive string) {
	t.Helper()
	var configErr *ConfigError
	if !errors.As(err, &configErr) || configErr.Field != field {
		t.Fatalf("want sanitized ConfigError for %s, got %T", field, err)
	}
	if strings.Contains(err.Error(), sensitive) || strings.Contains(err.Error(), "oceanbase://") {
		t.Fatal("migration error exposed input")
	}
}
