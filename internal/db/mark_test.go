package db

import "testing"

// Each engine's mark, which stands for it on a connection's badge, is two
// capital letters that no other engine has: a badge that two engines
// shared would tell them apart no better than one icon for all.
func TestEngineMarksAreDistinct(t *testing.T) {
	seen := map[string]Engine{}
	for e := range registeredRules {
		m := e.Mark()
		if len(m) != 2 || m[0] < 'A' || m[0] > 'Z' || m[1] < 'A' || m[1] > 'Z' {
			t.Errorf("%s has the mark %q, not two capital letters", e, m)
		}
		if other, ok := seen[m]; ok {
			t.Errorf("%s and %s share the mark %q", e, other, m)
		}
		seen[m] = e
	}
	if Engine("no-such-engine").Mark() != "DB" {
		t.Errorf("an engine no build knows has the mark %q", Engine("no-such-engine").Mark())
	}
}
