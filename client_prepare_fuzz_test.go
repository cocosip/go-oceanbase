package oceanbase

import (
	"database/sql/driver"
	"testing"
	"time"
)

func FuzzClientPrepare(f *testing.F) {
	for _, s := range []string{"a'\\中", "", "\x00", "\xff", "{fn now()}"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 {
			return
		}
		for _, mode := range []Mode{ModeMySQL, ModeOracle} {
			c := &conn{cfg: Config{Session: SessionOptions{Charset: testUTF8MB4, Location: time.UTC}, Limits: Limits{MaxMessageBytes: 1024, MaxLOBBytes: 512}}, session: &handshakeSession{mode: mode}}
			query, err := c.interpolate(selectBindSQL, []driver.NamedValue{{Value: value}})
			if err == nil && len(query) >= 1024 {
				t.Fatal("unbounded client query")
			}
		}
	})
}

func FuzzJDBCEscapes(f *testing.F) {
	for _, s := range []string{`SELECT {fn concat('x',{fn ucase(?)})}`, `{d '2026-01-01'}`, "{}", "{{{{", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, query string) {
		if len(query) > 4096 {
			return
		}
		for _, mode := range []Mode{ModeAuto, ModeMySQL, ModeOracle} {
			_, _ = RewriteJDBCEscapes(query, mode)
		}
	})
}
