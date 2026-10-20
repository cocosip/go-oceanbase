// Package integrationtest shares database profiles and error assertions for integration tests.
package integrationtest

import (
	context "context"
	sql "database/sql"
	errors "errors"
	os "os"
	runtime "runtime"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
)

// Profile identifies an optional real database environment.
type Profile struct {
	Name, Env string
	Mode      oceanbase.Mode
}

// SelectParameterSQL is the shared scalar parameter query.
const SelectParameterSQL = "SELECT ?"

// OracleDualSuffix selects an Oracle scalar expression from dual.
const OracleDualSuffix = " FROM dual"

// Profiles selects the requested direct or proxy test environments.
func Profiles(name string) ([]Profile, error) {
	all := []Profile{{"mysql", "OB_MYSQL_DSN", oceanbase.ModeMySQL}, {"oracle", "OB_ORACLE_DSN", oceanbase.ModeOracle}, {"proxy-mysql", "OBPROXY_MYSQL_DSN", oceanbase.ModeMySQL}, {"proxy-oracle", "OBPROXY_ORACLE_DSN", oceanbase.ModeOracle}}
	if name == "" || name == "all" {
		return all, nil
	}
	for _, p := range all {
		if p.Name == name {
			return []Profile{p}, nil
		}
	}
	return nil, errors.New("invalid integration profile")
}

// ProfileConfig reads and validates one configured environment.
func ProfileConfig(t *testing.T, p Profile) oceanbase.Config {
	t.Helper()
	dsn := os.Getenv(p.Env)
	if dsn == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required profile missing: " + p.Name)
		}
		t.Skip("profile not configured: " + p.Name)
	}
	cfg, err := oceanbase.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid DSN for profile: " + p.Name)
	}
	if cfg.Mode != oceanbase.ModeAuto && cfg.Mode != p.Mode {
		t.Fatal("DSN mode differs from profile: " + p.Name)
	}
	cfg.Mode = p.Mode
	return cfg
}

// OpenProfile opens a bounded pool and registers cleanup.
func OpenProfile(t *testing.T, p Profile) *sql.DB {
	t.Helper()
	cfg := ProfileConfig(t, p)
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid config for profile: " + p.Name)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error("close failed for profile: " + p.Name)
		}
	})
	t.Logf("profile=%s mode_constraint=%d go=%s os=%s arch=%s", p.Name, p.Mode, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return db
}

// TestContext creates the standard integration operation deadline.
func TestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// FailOperation reports the server code without exposing SQL or credentials.
func FailOperation(t *testing.T, operation string, err error) {
	t.Helper()
	var server *oceanbase.Error
	if errors.As(err, &server) {
		t.Fatalf("%s failed: server code=%d SQLSTATE=%s", operation, server.Code, server.SQLState)
	}
	t.Fatalf("%s failed: %T", operation, err)
}

// DuplicateKey accepts only the mode-specific duplicate server error.
func DuplicateKey(err error, mode oceanbase.Mode) bool {
	code := uint32(1062)
	if mode == oceanbase.ModeOracle {
		code = 1
	}
	return OnlyServerErrorCode(err, code)
}

// OnlyServerErrorCode requires every joined cause to be the expected server code.
func OnlyServerErrorCode(err error, code uint32, allowed ...error) bool {
	for _, sentinel := range allowed {
		if err == sentinel {
			return true
		}
	}
	if group, ok := err.(interface{ Unwrap() []error }); ok {
		causes := group.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !OnlyServerErrorCode(cause, code, allowed...) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return OnlyServerErrorCode(wrapped.Unwrap(), code, allowed...)
	}
	server, ok := err.(*oceanbase.Error)
	return ok && server != nil && server.Code == code
}

// XAFlagRejected identifies the established MySQL flag rejection response.
func XAFlagRejected(err error, mode oceanbase.Mode) bool {
	var server *oceanbase.Error
	return mode == oceanbase.ModeMySQL && errors.As(err, &server) && server != nil && OnlyServerErrorCode(err, 1398, oceanbase.ErrXA)
}
