package oceanbase

import (
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDSNJavaMigrationBoundary(t *testing.T) {
	for _, name := range []string{
		"createDB", "useSSL", "profileSQL", "enabledSSLCipherSuites",
		"trustCertificateKeyStorePassword", "trustCertificateKeyStoreUrl",
		"clientCertificateKeyStorePassword", "clientCertificateKeyStoreUrl",
		"trustCertificateKeyStoreType", "clientCertificateKeyStoreType",
		"DBNAME", "port", "disableMariaDbDriver", "allowLocalInfile", "nonMappedOptions",
		"password2", "password3",
	} {
		t.Run(name, func(t *testing.T) {
			assertMigrationOptionRejected(t, name)
		})
	}
}

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

func TestDSNJavaValueCoercions(t *testing.T) {
	const interactive = "interactive"
	for _, tc := range []struct{ name, value string }{
		{testConnectTimeoutOption, "30000"}, {"readTimeout", "1000"},
		{interactive, ""}, {interactive, "1"}, {interactive, "0"}, {interactive, "TrUe"},
		{"parseTime", "'true'"}, {"parseTime", `"true"`}, {"allowMultiStatements", "False"},
	} {
		t.Run(tc.name+"/"+tc.value, func(t *testing.T) {
			dsn := "oceanbase://app@localhost/demo?" + url.Values{tc.name: {tc.value}}.Encode()
			_, err := ParseDSN(dsn)
			field := tc.name
			if tc.value == "" {
				field = "DSN options"
			}
			assertMigrationConfigError(t, err, field, dsn)
		})
	}
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"0", 10 * time.Second}, {"0s", 10 * time.Second}, {"30s", 30 * time.Second}, {"30000ms", 30 * time.Second},
	} {
		cfg, err := ParseDSN("oceanbase://app@localhost/demo?connectTimeout=" + tc.value + "&readTimeout=0s&interactive=false")
		if err != nil || cfg.ConnectTimeout != tc.want || cfg.ReadTimeout != 0 || cfg.Interactive {
			t.Fatal("explicit Go duration or boolean semantics changed", err)
		}
	}
}

func TestDSNJavaDriverSelectionMarkerIsLiteral(t *testing.T) {
	const marker = "disableMariaDbDriver"
	u := url.URL{Scheme: "oceanbase", Host: "localhost:2883", Path: "/" + marker, User: url.UserPassword(marker, marker)}
	cfg, err := ParseDSN(u.String())
	if err != nil || cfg.User != marker || cfg.Password != marker || cfg.Database != marker || cfg.Addr != "localhost:2883" {
		t.Fatal("JDBC selection marker changed literal Go URL fields", err)
	}
	for _, dsn := range []string{
		"jdbc:oceanbase://localhost/demo?user=app&password=private",
		"jdbc:oceanbase:loadbalance://localhost/demo?user=app&password=private",
	} {
		_, err := ParseDSN(dsn)
		assertMigrationConfigError(t, err, "DSN", "private")
	}
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
