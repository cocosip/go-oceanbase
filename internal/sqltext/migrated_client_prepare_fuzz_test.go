package sqltext

import (
	"github.com/cocosip/go-oceanbase/config"
	"testing"
)

func FuzzJDBCEscapes(f *testing.F) {
	for _, s := range []string{`SELECT {fn concat('x',{fn ucase(?)})}`, `{d '2026-01-01'}`, "{}", "{{{{", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, query string) {
		if len(query) > 4096 {
			return
		}
		for _, mode := range []config.Mode{config.ModeAuto, config.ModeMySQL, config.ModeOracle} {
			_, _ = RewriteJDBCEscapes(query, mode)
		}
	})
}
