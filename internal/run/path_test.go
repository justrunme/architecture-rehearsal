package run

import "testing"

func TestPathEmptyRefStaysEmpty(t *testing.T) {
	e := &Engine{WorkDir: "/var/lib/rehearsal"}
	if got := e.path(""); got != "" {
		t.Fatalf("empty ref joined with workdir: %q", got)
	}
	if got := e.path("baseline.json"); got != "/var/lib/rehearsal/baseline.json" {
		t.Fatalf("relative: %q", got)
	}
	if got := e.path("/abs/x"); got != "/abs/x" {
		t.Fatalf("abs: %q", got)
	}
}
