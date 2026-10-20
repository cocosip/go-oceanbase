package command

import (
	"context"
	"database/sql/driver"
	"math"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testClientCharset = "utf8mb4"

const testClientQuotedText = "a'\\中文"
const selectBindSQL = "SELECT ?"
const testMaxUint64Text = "18446744073709551615"

func clientOperation(t *testing.T, cfg config.Config, protocol session.Negotiated) *session.Operation {
	t.Helper()
	s := session.New(cfg, &protocol)
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return op
}

func interpolateClient(op *session.Operation, query string, values []driver.NamedValue) (string, error) {
	data, err := Interpolate(op, query, values)
	return string(data), err
}

func TestClientPrepareEncoding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   config.Mode
		status uint16
		value  any
		want   string
	}{
		{"mysql-quote", config.ModeMySQL, 2, testClientQuotedText, `_utf8mb4 X'61275ce4b8ade69687'`},
		{"mysql-no-backslash", config.ModeMySQL, 514, testClientQuotedText, `_utf8mb4 X'61275ce4b8ade69687'`},
		{"oracle-quote", config.ModeOracle, 6, testClientQuotedText, `'a''\中文'`},
		{"mysql-bytes", config.ModeMySQL, 2, []byte{0, 39, 255}, "X'0027ff'"},
		{"oracle-bytes", config.ModeOracle, 6, []byte{0, 39, 255}, "HEXTORAW('0027ff')"},
		{"nil-bytes", config.ModeMySQL, 2, []byte(nil), "NULL"},
		{"empty-bytes", config.ModeMySQL, 2, []byte{}, "X''"},
		{"null", config.ModeOracle, 6, nil, "NULL"},
		{"uint64", config.ModeMySQL, 2, uint64(math.MaxUint64), testMaxUint64Text},
		{"bool", config.ModeOracle, 6, true, "1"},
		{"oracle-small-double", config.ModeOracle, 6, float64(1e-200), "1e-200d"},
		{"oracle-large-double", config.ModeOracle, 6, float64(1e200), "1e+200d"},
		{"mysql-double", config.ModeMySQL, 2, float64(0.1), "0.1e0"},
		{"date", config.ModeOracle, 6, time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC), "TIMESTAMP '2026-01-02 03:04:05.123456'"},
		{"oracle-nul", config.ModeOracle, 6, "a\x00b", "('a'||CHR(0)||'b')"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := clientOperation(t, config.Config{Session: config.SessionOptions{Charset: testClientCharset, Location: time.UTC}, Limits: config.Limits{MaxMessageBytes: 1024, MaxLOBBytes: 1024}}, session.Negotiated{Mode: tc.mode, OK: wire.OK{Status: tc.status}})
			got, err := interpolateClient(op, selectBindSQL, []driver.NamedValue{{Ordinal: 1, Value: tc.value}})
			if err != nil || got != "SELECT  "+tc.want+" " {
				t.Fatalf("%q %v", got, err)
			}
		})
	}
}

func TestClientPrepareTokenBoundary(t *testing.T) {
	op := clientOperation(t, config.Config{Session: config.SessionOptions{Charset: testClientCharset}, Limits: config.Limits{MaxMessageBytes: 1024}}, session.Negotiated{Mode: config.ModeOracle})
	for _, tc := range []struct {
		value any
		want  string
	}{{int64(-1), "SELECT - -1  FROM dual"}, {float64(-1), "SELECT - -1d  FROM dual"}} {
		query, err := interpolateClient(op, "SELECT -:x FROM dual", []driver.NamedValue{{Name: "x", Value: tc.value}})
		if err != nil || query != tc.want {
			t.Fatalf("token boundary changed: %q %v", query, err)
		}
	}
	query, err := interpolateClient(op, "SELECT :x e3 FROM dual", []driver.NamedValue{{Name: "x", Value: int64(1)}})
	if err != nil || query != "SELECT  1  e3 FROM dual" {
		t.Fatal(query, err)
	}
}

func TestClientPrepareCharset(t *testing.T) {
	op := clientOperation(t, config.Config{Session: config.SessionOptions{Charset: "latin1"}, Limits: config.Limits{MaxMessageBytes: 1024}}, session.Negotiated{Mode: config.ModeOracle})
	query, err := interpolateClient(op, "SELECT ?, 'é'", []driver.NamedValue{{Value: "€"}})
	want := string([]byte{'S', 'E', 'L', 'E', 'C', 'T', ' ', ' ', '\'', 128, '\'', ' ', ',', ' ', '\'', 233, '\''})
	if err != nil || query != want {
		t.Fatalf("%x want %x: %v", query, want, err)
	}
}
