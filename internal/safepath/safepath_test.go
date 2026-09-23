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

func TestHumanTimecodeIsReadableAndDropsZone(t *testing.T) {
	if got := HumanTimecode("2026-07-15T14:30:12+02:00"); got != "2026-07-15_14-30-12" {
		t.Fatalf("HumanTimecode = %q, want a readable stamp with the offset dropped", got)
	}
	if got := HumanTimecode("2026-08-31T07:39:43Z"); got != "2026-08-31_07-39-43" {
		t.Fatalf("HumanTimecode = %q, want the trailing Z dropped like any other zone", got)
	}
}

// TestTimeslotStampDedupesSameDayEnd covers the export filename's whole
// point: two segments for the same camera on the same day must not collapse
// to the same name, and a same-day end time shouldn't repeat the date the
// start already gave.
func TestTimeslotStampDedupesSameDayEnd(t *testing.T) {
	if got := TimeslotStamp("2026-09-21T10:58:30Z", "2026-09-21T11:36:03Z"); got != "2026-09-21_10-58-30_to_11-36-03" {
		t.Fatalf("TimeslotStamp same-day = %q", got)
	}
	if got := TimeslotStamp("2026-09-21T23:58:30Z", "2026-09-22T00:05:00Z"); got != "2026-09-21_23-58-30_to_2026-09-22_00-05-00" {
		t.Fatalf("TimeslotStamp crossing midnight = %q, want the end date kept", got)
	}
	if got := TimeslotStamp("2026-09-21T10:58:30Z", ""); got != "2026-09-21_10-58-30" {
		t.Fatalf("TimeslotStamp with no end = %q", got)
	}
}
