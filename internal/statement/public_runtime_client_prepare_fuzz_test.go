package statement_test

import (
	driver "database/sql/driver"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
)

func FuzzClientPrepare(f *testing.F) {
	for _, s := range []string{"a'\\中", "", "\x00", "\xff", "{fn now()}"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 {
			return
		}
		for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
			cfg, mode := oceanbase.Config{Session: oceanbase.SessionOptions{Charset: testUTF8MB4, Location: time.UTC}, Limits: oceanbase.Limits{MaxMessageBytes: 1024, MaxLOBBytes: 512}}, mode
			query, err := interpolateStatement(t, cfg, mode, selectBindSQL, []driver.NamedValue{{Value: value}})
			if err == nil && len(query) >= 1024 {
				t.Fatal("unbounded client query")
			}
		}
	})
}
