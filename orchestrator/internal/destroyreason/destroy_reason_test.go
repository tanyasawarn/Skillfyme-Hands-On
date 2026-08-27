package destroyreason

import "testing"

// PLAN.md K19: pins the exact string values every real producer
// (idledetect, main.go's budget hard-stop, the reaper) used
// independently before this consolidation, and the wire value
// practice-core sends for a clean submit.
func TestDestroyReasons_MatchOriginalStringValues(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Idle", Idle, "idle"},
		{"Budget", Budget, "budget"},
		{"Reaper", Reaper, "reaper"},
		{"Submit", Submit, "submit"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestDestroyReasons_AreDistinct(t *testing.T) {
	values := []string{Idle, Budget, Reaper, Submit}
	seen := make(map[string]bool)
	for _, v := range values {
		if seen[v] {
			t.Errorf("duplicate destroy-reason value: %q", v)
		}
		seen[v] = true
	}
}
