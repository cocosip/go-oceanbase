package sqltext

import (
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"testing"
)

func FuzzBlobIdentifiers(f *testing.F) {
	for _, s := range []string{"plain", "a`?;--", "a\"?;--", "a\\`", "中.文", "\x00", "\xff", "?"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 1024 || !valueformat.Identifier(name) {
			return
		}
		for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
			id := QuoteIdentifier(mode, name)
			for _, noBackslash := range []bool{false, true} {
				plan, err := BuildBindPlanEscapes("SELECT "+id+" FROM "+id+"."+id+" WHERE "+id+" = ?", mode, noBackslash)
				if err != nil || plan.NumInput != 1 || len(plan.Slots) != 1 {
					t.Fatalf("quoted identifier changed SQL bindings: mode %v name %q: %v", mode, name, err)
				}
			}
		}
	})
}
