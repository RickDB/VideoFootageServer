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
