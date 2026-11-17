package notify_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/notify"
)

func TestBuildBodyNamesLotPlateAndStartTime(t *testing.T) {
	notice := domain.CheckInNotice{
		SessionID:   "session-1",
		LotName:     "Central Plaza",
		Plate:       "KA 01 AB 1234",
		Phone:       "+14155552671",
		CheckedInAt: time.Date(2026, time.February, 14, 9, 30, 0, 0, time.UTC),
	}

	body := notify.BuildBody(notice)

	want := "Central Plaza: KA 01 AB 1234 checked in at 2026-02-14T09:30:00Z"
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if strings.Contains(body, "...") {
		t.Errorf("body = %q, want no shortening for a short lot name", body)
	}
}

func TestBuildBodyTruncatesLongLotName(t *testing.T) {
	notice := domain.CheckInNotice{
		SessionID:   "session-1",
		LotName:     strings.Repeat("A", 200),
		Plate:       "KA 01 AB 1234",
		Phone:       "+14155552671",
		CheckedInAt: time.Date(2026, time.February, 14, 9, 30, 0, 0, time.UTC),
	}

	body := notify.BuildBody(notice)

	if length := utf8.RuneCountInString(body); length > notify.MaxBodyLength {
		t.Errorf("body length = %d, want at most %d", length, notify.MaxBodyLength)
	}

	shortened, rest, ok := strings.Cut(body, "...")
	if !ok {
		t.Fatalf("body = %q, want the lot name to end with an ellipsis", body)
	}
	if shortened == "" || strings.Trim(shortened, "A") != "" {
		t.Errorf("lot name part = %q, want a shortened run of the original name", shortened)
	}
	if !strings.Contains(rest, "KA 01 AB 1234") {
		t.Errorf("body = %q, want the plate after the shortened lot name", body)
	}
	if !strings.Contains(rest, "2026-02-14T09:30:00Z") {
		t.Errorf("body = %q, want the RFC 3339 UTC start time", body)
	}
}
