package sqltext

import "testing"

func TestNamedRoutineInvalidGrammar(t *testing.T) {
	for _, text := range []string{"SELECT ?", "CALL p(?); SELECT 1", "CALL p(?, NOW())", "CALL p(? + 1)", "CALL p(?,)", "CALL p(?,@variable)", "CALL p(?,(2))", "CALL p(?,\"x\")", "CALL p(?,/*!1*/2)", "CALL app..p(?)"} {
		if _, err := NamedRoutine(text, false); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	for _, text := range []string{"CALL named_routine(?)", "/*c*/ call `a``b`.`p`(1, ?, 'x,y', NULL, -2e+3); -- c", "CALL p(?, TRUE, .5)"} {
		if _, err := NamedRoutine(text, false); err != nil {
			t.Errorf("rejected %q: %v", text, err)
		}
	}
}
