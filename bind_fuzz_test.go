package oceanbase

import "testing"

func FuzzBindPlan(f *testing.F) {
	f.Add([]byte("SELECT :x, :x FROM dual"))
	f.Add([]byte("BEGIN :x := q'[?:hidden]'; END;"))
	f.Add([]byte("SELECT ?, 'a\\\\b', `?` /* ? */"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			t.Skip()
		}
		for _, mode := range []Mode{ModeMySQL, ModeOracle} {
			for _, noBackslash := range []bool{false, true} {
				p, err := buildBindPlanSQLMode(string(data), mode, noBackslash, len(data)%2 == 0)
				if err != nil {
					continue
				}
				last := 0
				for _, s := range p.slots {
					if s.start < last || s.end <= s.start || s.end > len(data) || s.index < 0 || s.index >= p.numInput {
						t.Fatal("invalid binding span")
					}
					last = s.end
				}
			}
		}
	})
}
