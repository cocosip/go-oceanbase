package statement_test

import (
	driver "database/sql/driver"
	errors "errors"
	math "math"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
)

func TestClientPrepareReject(t *testing.T) {
	cfg, mode := oceanbase.Config{Session: oceanbase.SessionOptions{Charset: testUTF8MB4, Location: time.UTC}, Limits: oceanbase.Limits{MaxMessageBytes: 64, MaxLOBBytes: 32}}, oceanbase.ModeMySQL
	for _, tc := range []struct {
		query string
		value any
	}{
		{`SELECT "a\", ?, "b"`, int64(1)},
		{selectBindSQL, math.Inf(1)},
		{selectBindSQL, string([]byte{255})},
		{selectBindSQL, string(make([]byte, 100))},
		{selectBindSQL, make([]byte, 33)},
	} {
		if _, err := interpolateStatement(t, cfg, mode, tc.query, []driver.NamedValue{{Value: tc.value}}); err == nil {
			t.Fatal("unsafe client binding accepted")
		}
	}
	if _, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, PrepareMode: 99}); err == nil {
		t.Fatal("invalid prepare mode")
	}
	cfg, err := oceanbase.ParseDSN("oceanbase://app@localhost/db?prepareMode=client")
	if err != nil || cfg.PrepareMode != oceanbase.PrepareClient {
		t.Fatal(err)
	}
	if _, err = oceanbase.ParseDSN("oceanbase://app@localhost/db?prepareMode=unknown"); err == nil {
		t.Fatal("invalid DSN strategy")
	}
	_, err = interpolateStatement(t, cfg, mode, "SELECT ? /* ? */", nil)
	if !errors.Is(err, oceanbase.ErrBind) {
		t.Fatal(err)
	}
}
