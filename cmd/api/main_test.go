package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/techverito/parking-service/internal/config"
	"github.com/techverito/parking-service/internal/notify"
)

func TestNewNotifierSelectsImplementation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("no twilio settings", func(t *testing.T) {
		notifier := newNotifier(config.Config{}, logger)
		if _, ok := notifier.(*notify.LogNotifier); !ok {
			t.Errorf("notifier = %T, want *notify.LogNotifier", notifier)
		}
	})

	t.Run("partial twilio settings", func(t *testing.T) {
		notifier := newNotifier(config.Config{
			TwilioAccountSID: "AC00000000000000000000000000000000",
			TwilioFromNumber: "+14155552671",
		}, logger)
		if _, ok := notifier.(*notify.LogNotifier); !ok {
			t.Errorf("notifier = %T, want *notify.LogNotifier", notifier)
		}
	})

	t.Run("all twilio settings", func(t *testing.T) {
		notifier := newNotifier(config.Config{
			TwilioAccountSID: "AC00000000000000000000000000000000",
			TwilioAuthToken:  "auth-token",
			TwilioFromNumber: "+14155552671",
		}, logger)
		if _, ok := notifier.(*notify.TwilioNotifier); !ok {
			t.Errorf("notifier = %T, want *notify.TwilioNotifier", notifier)
		}
	})
}
