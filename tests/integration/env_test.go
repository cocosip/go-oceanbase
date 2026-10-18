//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

type profile struct {
	name, env string
	mode      oceanbase.Mode
}

const selectParameterSQL = "SELECT ?"

const oracleDualSuffix = " FROM dual"

func profiles(name string) ([]profile, error) {
	all := []profile{{"mysql", "OB_MYSQL_DSN", oceanbase.ModeMySQL}, {"oracle", "OB_ORACLE_DSN", oceanbase.ModeOracle}, {"proxy-mysql", "OBPROXY_MYSQL_DSN", oceanbase.ModeMySQL}, {"proxy-oracle", "OBPROXY_ORACLE_DSN", oceanbase.ModeOracle}}
	if name == "" || name == "all" {
		return all, nil
	}
	for _, p := range all {
		if p.name == name {
			return []profile{p}, nil
		}
	}
	return nil, errors.New("invalid integration profile")
}

func profileConfig(t *testing.T, p profile) oceanbase.Config {
	t.Helper()
	dsn := os.Getenv(p.env)
	if dsn == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required profile missing: " + p.name)
		}
		t.Skip("profile not configured: " + p.name)
	}
	cfg, err := oceanbase.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid DSN for profile: " + p.name)
	}
	if cfg.Mode != oceanbase.ModeAuto && cfg.Mode != p.mode {
		t.Fatal("DSN mode differs from profile: " + p.name)
	}
	cfg.Mode = p.mode
	return cfg
}

func openProfile(t *testing.T, p profile) *sql.DB {
	t.Helper()
	cfg := profileConfig(t, p)
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid config for profile: " + p.name)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error("close failed for profile: " + p.name)
		}
	})
	t.Logf("profile=%s mode_constraint=%d go=%s os=%s arch=%s", p.name, p.mode, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return db
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func failOperation(t *testing.T, operation string, err error) {
	t.Helper()
	var server *oceanbase.Error
	if errors.As(err, &server) {
		t.Fatalf("%s failed: server code=%d SQLSTATE=%s", operation, server.Code, server.SQLState)
	}
	t.Fatalf("%s failed: %T", operation, err)
}

func duplicateKey(err error, mode oceanbase.Mode) bool {
	code := uint32(1062)
	if mode == oceanbase.ModeOracle {
		code = 1
	}
	return onlyServerErrorCode(err, code)
}

func onlyServerErrorCode(err error, code uint32) bool {
	if group, ok := err.(interface{ Unwrap() []error }); ok {
		causes := group.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyServerErrorCode(cause, code) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyServerErrorCode(wrapped.Unwrap(), code)
	}
	server, ok := err.(*oceanbase.Error)
	return ok && server != nil && server.Code == code
}

func TestDuplicateKeyErrors(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, code := range []uint32{0, 1, 1062, 1064, 1031, 942} {
			want := mode == oceanbase.ModeMySQL && code == 1062 || mode == oceanbase.ModeOracle && code == 1
			server := &oceanbase.Error{Code: code}
			for _, err := range []error{server, fmt.Errorf("wrapped: %w", server), &oceanbase.BatchError{Index: 1, Err: server}, errors.Join(server, server)} {
				if duplicateKey(err, mode) != want {
					t.Errorf("mode=%d code=%d: duplicate classification mismatch", mode, code)
				}
			}
		}
		for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, (*oceanbase.Error)(nil)} {
			if duplicateKey(err, mode) {
				t.Errorf("mode=%d: non-server error accepted as duplicate", mode)
			}
		}
		for _, code := range []uint32{1, 1062} {
			if duplicateKey(errors.Join(&oceanbase.Error{Code: code}, context.DeadlineExceeded), mode) {
				t.Error("joined unrelated failure accepted as duplicate")
			}
		}
	}
}

func TestExpectedServerErrors(t *testing.T) {
	for _, code := range []uint32{1, 1062, 1644, 20001, 1317, 1013} {
		server := &oceanbase.Error{Code: code}
		other := &oceanbase.Error{Code: code + 1}
		for _, tc := range []struct {
			name string
			err  error
			want bool
		}{
			{"direct", server, true},
			{"wrapped", fmt.Errorf("wrapped: %w", server), true},
			{"joined-matching", errors.Join(server, server), true},
			{"nested-matching", fmt.Errorf("wrapped: %w", errors.Join(server, fmt.Errorf("nested: %w", server))), true},
			{"nil", nil, false},
			{"typed-nil", (*oceanbase.Error)(nil), false},
			{"timeout", context.DeadlineExceeded, false},
			{"other-code", other, false},
			{"joined-timeout", errors.Join(server, context.DeadlineExceeded), false},
			{"joined-cleanup", errors.Join(server, errors.New("cleanup failed")), false},
			{"joined-other-code", errors.Join(server, other), false},
			{"joined-other-first", errors.Join(other, server), false},
			{"joined-typed-nil", errors.Join(server, (*oceanbase.Error)(nil)), false},
			{"wrapped-joined-timeout", fmt.Errorf("wrapped: %w", errors.Join(server, context.DeadlineExceeded)), false},
		} {
			t.Run(fmt.Sprintf("%d/%s", code, tc.name), func(t *testing.T) {
				if got := onlyServerErrorCode(tc.err, code); got != tc.want {
					t.Fatalf("server error classification = %v; want %v", got, tc.want)
				}
			})
		}
	}
}

func TestProfiles(t *testing.T) {
	all, err := profiles("all")
	if err != nil || len(all) != 4 {
		t.Fatal("profile matrix incomplete")
	}
	for _, name := range []string{"mysql", "oracle", "proxy-mysql", "proxy-oracle"} {
		items, err := profiles(name)
		if err != nil || len(items) != 1 || items[0].name != name || items[0].env == "" {
			t.Fatal("incorrect profile selection")
		}
	}
	if _, err = profiles("unexpected"); err == nil {
		t.Fatal("invalid profile silently selected")
	}
}
