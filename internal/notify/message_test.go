package notify_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/techverito/parking-service/internal/notify"
)

var _ notify.Notifier = (*notify.LogNotifier)(nil)

func TestCheckInMessageFormat(t *testing.T) {
	start := time.Date(2024, time.May, 1, 10, 15, 0, 0, time.UTC)

	got := notify.CheckInMessage("Central Plaza", "KA 01 AB 1234", start)
	want := "Checked in to Central Plaza. Plate KA 01 AB 1234. Start 2024-05-01T10:15:00Z."
	if got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestCheckInMessageTruncatesLongLotName(t *testing.T) {
	lotName := strings.Repeat("Long Lot Name ", 22)
	plate := "KA 01 AB 1234 XY"
	start := time.Date(2024, time.May, 1, 10, 15, 0, 0, time.UTC)

	got := notify.CheckInMessage(lotName, plate, start)

	if length := utf8.RuneCountInString(got); length > notify.MaxMessageLength {
		t.Errorf("message length = %d, want <= %d", length, notify.MaxMessageLength)
	}
	if !strings.Contains(got, ". Plate "+plate+".") {
		t.Errorf("message = %q, want the plate kept verbatim", got)
	}
	if !strings.HasSuffix(got, "Start 2024-05-01T10:15:00Z.") {
		t.Errorf("message = %q, want the RFC 3339 start time at the end", got)
	}
	if !strings.HasPrefix(got, "Checked in to ") {
		t.Errorf("message = %q, want the usual prefix", got)
	}
	if !strings.Contains(got, "...") {
		t.Errorf("message = %q, want the shortened lot name marked with an ellipsis", got)
	}
}

func TestLogNotifierLogsAndReturnsNil(t *testing.T) {
	var buffer bytes.Buffer
	notifier := notify.NewLogNotifier(slog.New(slog.NewTextHandler(&buffer, nil)))

	err := notifier.Send(context.Background(), notify.Message{
		To:   "+14155550123",
		Body: "Checked in to Central Plaza.",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	logged := buffer.String()
	if !strings.Contains(logged, "+14155550123") {
		t.Errorf("log = %q, want the recipient", logged)
	}
	if !strings.Contains(logged, "Checked in to Central Plaza.") {
		t.Errorf("log = %q, want the message body", logged)
	}
}
