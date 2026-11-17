package notify_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/notify"
)

func testNotice() domain.CheckInNotice {
	return domain.CheckInNotice{
		SessionID:   "session-1",
		LotName:     "Central Plaza",
		Plate:       "KA 01 AB 1234",
		Phone:       "+14155552671",
		CheckedInAt: time.Date(2026, time.February, 14, 9, 30, 0, 0, time.UTC),
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingSender stands in for Twilio and records what it was asked to send.
type recordingSender struct {
	calls     int
	to        string
	body      string
	err       error
	block     bool
	remaining time.Duration
}

func (s *recordingSender) Send(ctx context.Context, to string, body string) error {
	s.calls++
	s.to = to
	s.body = body
	if deadline, ok := ctx.Deadline(); ok {
		s.remaining = time.Until(deadline)
	}
	if s.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.err
}

func TestNewSelectsLogNotifierWhenSettingsIncomplete(t *testing.T) {
	cases := []struct {
		name     string
		settings notify.Settings
	}{
		{name: "no settings", settings: notify.Settings{}},
		{name: "account sid only", settings: notify.Settings{AccountSID: "AC00000000000000000000000000000000"}},
		{name: "blank values", settings: notify.Settings{AccountSID: " ", AuthToken: " ", FromNumber: " "}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notifier := notify.New(tc.settings, discardLogger())
			if _, ok := notifier.(*notify.LogNotifier); !ok {
				t.Fatalf("notifier = %T, want *notify.LogNotifier", notifier)
			}
		})
	}
}

func TestNewSelectsTwilioNotifierWhenSettingsComplete(t *testing.T) {
	notifier := notify.New(notify.Settings{
		AccountSID: "AC00000000000000000000000000000000",
		AuthToken:  "test-auth-token",
		FromNumber: "+15017122661",
	}, discardLogger())

	if _, ok := notifier.(*notify.TwilioNotifier); !ok {
		t.Fatalf("notifier = %T, want *notify.TwilioNotifier", notifier)
	}
}

func TestLogNotifierLogsBody(t *testing.T) {
	var buffer bytes.Buffer
	notifier := notify.NewLogNotifier(slog.New(slog.NewTextHandler(&buffer, nil)))

	if err := notifier.NotifyCheckIn(context.Background(), testNotice()); err != nil {
		t.Fatalf("notify: %v", err)
	}

	record := buffer.String()
	for _, want := range []string{"Central Plaza", "KA 01 AB 1234", "+14155552671", "session-1"} {
		if !strings.Contains(record, want) {
			t.Errorf("log record = %q, want it to contain %q", record, want)
		}
	}
}

func TestTwilioNotifierSendsBuiltBody(t *testing.T) {
	sender := &recordingSender{}
	notifier := notify.NewTwilioNotifierWithSender(sender, "+15017122661", discardLogger())

	notice := testNotice()
	if err := notifier.NotifyCheckIn(context.Background(), notice); err != nil {
		t.Fatalf("notify: %v", err)
	}

	if sender.calls != 1 {
		t.Fatalf("send calls = %d, want exactly one", sender.calls)
	}
	if sender.to != notice.Phone {
		t.Errorf("to = %q, want %q", sender.to, notice.Phone)
	}
	if want := notify.BuildBody(notice); sender.body != want {
		t.Errorf("body = %q, want %q", sender.body, want)
	}
}

func TestTwilioNotifierAppliesFiveSecondTimeout(t *testing.T) {
	sender := &recordingSender{block: true}
	notifier := notify.NewTwilioNotifierWithSender(sender, "+15017122661", discardLogger())

	err := notifier.NotifyCheckIn(context.Background(), testNotice())
	if err == nil {
		t.Fatal("expected an error once the send deadline passes")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if sender.remaining <= 4*time.Second || sender.remaining > 5*time.Second {
		t.Errorf("send deadline = %v away, want more than 4s and at most 5s", sender.remaining)
	}
}

func TestTwilioNotifierReturnsProviderError(t *testing.T) {
	providerErr := errors.New("twilio rejected the message")
	sender := &recordingSender{err: providerErr}
	notifier := notify.NewTwilioNotifierWithSender(sender, "+15017122661", discardLogger())

	err := notifier.NotifyCheckIn(context.Background(), testNotice())
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want it to wrap the provider error", err)
	}
	if sender.calls != 1 {
		t.Errorf("send calls = %d, want exactly one with no retry", sender.calls)
	}
}
