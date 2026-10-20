package sqltext

import (
	"github.com/cocosip/go-oceanbase/config"
	"testing"
)

func FuzzBindPlan(f *testing.F) {
	f.Add([]byte("SELECT :x, :x FROM dual"))
	f.Add([]byte("BEGIN :x := q'[?:hidden]'; END;"))
	f.Add([]byte("SELECT ?, 'a\\\\b', `?` /* ? */"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			t.Skip()
		}
		for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
			for _, noBackslash := range []bool{false, true} {
				p, err := BuildBindPlanSQLMode(string(data), mode, noBackslash, len(data)%2 == 0)
				if err != nil {
					continue
				}
				last := 0
				for _, s := range p.Slots {
					if s.Start < last || s.End <= s.Start || s.End > len(data) || s.Index < 0 || s.Index >= p.NumInput {
						t.Fatal("invalid binding span")
					}
					last = s.End
				}
			}
		}
	})
}
