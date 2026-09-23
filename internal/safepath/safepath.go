package safepath

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	invalidComponent = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
	spaces           = regexp.MustCompile(`\s+`)
	windowsReserved  = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$`)
)

// Component returns a filename component that is valid on Windows and Linux.
// It preserves readable spaces while removing separators, control characters,
// trailing spaces/dots, and Windows device names.
func Component(value, fallback string) string {
	value = invalidComponent.ReplaceAllString(value, "-")
	value = spaces.ReplaceAllString(strings.TrimSpace(value), " ")
	value = strings.Trim(value, " .-")
	if value == "" {
		value = fallback
	}
	if windowsReserved.MatchString(value) {
		value = "_" + value
	}
	for utf8.RuneCountInString(value) > 120 {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	value = strings.TrimRight(value, " .")
	if value == "" {
		value = fallback
	}
	return value
}

func WallTimecode(value string) string {
	clock := value
	if len(clock) >= 19 && clock[4] == '-' && clock[7] == '-' && clock[10] == 'T' {
		clock = clock[:19]
	}
	clock = strings.NewReplacer("-", "", ":", "").Replace(clock)
	return Component(clock, "unknown-time")
}

// HumanTimecode turns a recorder wall-clock value ("2026-09-21T10:58:30...")
// into a filename-safe, human-readable stamp ("2026-09-21_10-58-30"). Unlike
// WallTimecode it keeps the date/time separators (as dashes and an
// underscore) so the exported filename reads clearly to whoever is
// reviewing the footage, instead of a run-together digit string. Any
// trailing zone offset or "Z" designator is dropped: the recorder's own
// clock is what the filename should reflect, not UTC bookkeeping.
func HumanTimecode(value string) string {
	clock := value
	if len(clock) >= 19 && clock[4] == '-' && clock[7] == '-' && clock[10] == 'T' {
		clock = clock[:19]
	}
	clock = strings.Replace(clock, "T", "_", 1)
	clock = strings.ReplaceAll(clock, ":", "-")
	return Component(clock, "unknown-time")
}

// TimeslotStamp renders a start/end pair as the timeslot part of an exported
// filename, e.g. "2026-09-21_10-58-30_to_11-30-00". When both ends fall on
// the same calendar day the end's date is dropped rather than repeated, so
// the reviewer's eye goes straight to the two times that actually differ.
func TimeslotStamp(start, end string) string {
	startStamp := HumanTimecode(start)
	if end == "" {
		return startStamp
	}
	endStamp := HumanTimecode(end)
	if len(startStamp) >= 11 && len(endStamp) >= 11 && startStamp[:10] == endStamp[:10] {
		return startStamp + "_to_" + endStamp[11:]
	}
	return startStamp + "_to_" + endStamp
}
