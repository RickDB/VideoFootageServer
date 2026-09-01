package safepath

import "testing"

func TestComponentIsPortable(t *testing.T) {
	tests := map[string]string{
		`CASE:42 / North*`: "CASE-42 - North",
		`CON`:              "_CON",
		`  evidence... `:   "evidence",
	}
	for input, want := range tests {
		if got := Component(input, "fallback"); got != want {
			t.Errorf("Component(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWallTimecodePreservesRecorderClock(t *testing.T) {
	if got := WallTimecode("2026-07-15T14:30:12+02:00"); got != "20260715T143012" {
		t.Fatalf("WallTimecode shifted or malformed: %q", got)
	}
}
