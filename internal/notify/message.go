package notify

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// MaxMessageLength is one SMS segment. Every composed message stays at or below
// this many runes.
const MaxMessageLength = 160

// ellipsis marks a shortened lot name. ASCII rather than U+2026 keeps the body
// inside the GSM-7 alphabet, and therefore inside a single segment.
const ellipsis = "..."

// CheckInMessage composes the confirmation for a new session. Only the lot name
// is ever shortened — and only when the message would not fit in one segment —
// so the plate and the start time always survive intact.
func CheckInMessage(lotName, plate string, start time.Time) string {
	body := composeCheckInMessage(lotName, plate, start)
	if utf8.RuneCountInString(body) <= MaxMessageLength {
		return body
	}

	fixed := fmt.Sprintf("Checked in to . Plate %s. Start %s.", plate, start.UTC().Format(time.RFC3339))
	budget := MaxMessageLength - utf8.RuneCountInString(fixed) - utf8.RuneCountInString(ellipsis)

	return composeCheckInMessage(truncateRunes(lotName, budget)+ellipsis, plate, start)
}

func composeCheckInMessage(lotName, plate string, start time.Time) string {
	return fmt.Sprintf("Checked in to %s. Plate %s. Start %s.", lotName, plate, start.UTC().Format(time.RFC3339))
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}
