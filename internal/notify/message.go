// Package notify delivers check-in confirmations to drivers. It holds the real
// Twilio sender and the log-only fallback used when Twilio is not configured.
package notify

import (
	"time"
	"unicode/utf8"

	"github.com/techverito/parking-service/internal/domain"
)

// MaxBodyLength is one SMS segment in GSM-7 encoding.
const MaxBodyLength = 160

// smsEllipsis marks a shortened lot name. Three ASCII dots keep the body
// GSM-7 encodable: a single U+2026 would force UCS-2, where one segment is
// only 70 characters.
const smsEllipsis = "..."

// BuildBody renders the confirmation message: the lot, the plate and the start
// time as RFC 3339 UTC. When the message would need more than one segment the
// lot name is shortened with an ellipsis so the body fits in MaxBodyLength
// runes.
func BuildBody(notice domain.CheckInNotice) string {
	suffix := ": " + notice.Plate + " checked in at " + notice.CheckedInAt.UTC().Format(time.RFC3339)

	body := notice.LotName + suffix
	if utf8.RuneCountInString(body) <= MaxBodyLength {
		return body
	}

	budget := MaxBodyLength - utf8.RuneCountInString(suffix) - utf8.RuneCountInString(smsEllipsis)
	if budget < 0 {
		return truncate(body, MaxBodyLength)
	}
	return truncate(notice.LotName, budget) + smsEllipsis + suffix
}

// truncate keeps at most limit runes of value.
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
