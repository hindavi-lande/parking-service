package notify_test

import (
	"context"
	"errors"
	"testing"

	api "github.com/twilio/twilio-go/rest/api/v2010"

	"github.com/techverito/parking-service/internal/notify"
)

var _ notify.Notifier = (*notify.TwilioNotifier)(nil)

// fakeTwilioSender records the params it receives and returns the configured
// error, so the notifier is exercised without any network call.
type fakeTwilioSender struct {
	params *api.CreateMessageParams
	err    error
}

func (f *fakeTwilioSender) CreateMessageWithContext(_ context.Context, params *api.CreateMessageParams) error {
	f.params = params
	return f.err
}

func fullSettings() notify.TwilioSettings {
	return notify.TwilioSettings{
		AccountSID: "AC00000000000000000000000000000000",
		AuthToken:  "test-auth-token",
		FromNumber: "+15005550006",
	}
}

func TestFromSettingsReturnsTwilioNotifierWhenConfigured(t *testing.T) {
	notifier := notify.FromSettings(fullSettings(), nil)

	if _, ok := notifier.(*notify.TwilioNotifier); !ok {
		t.Fatalf("notifier = %T, want *notify.TwilioNotifier", notifier)
	}
}

func TestFromSettingsFallsBackToLogNotifier(t *testing.T) {
	complete := fullSettings()

	cases := []struct {
		name     string
		settings notify.TwilioSettings
	}{
		{
			name:     "account sid missing",
			settings: notify.TwilioSettings{AuthToken: complete.AuthToken, FromNumber: complete.FromNumber},
		},
		{
			name:     "auth token missing",
			settings: notify.TwilioSettings{AccountSID: complete.AccountSID, FromNumber: complete.FromNumber},
		},
		{
			name:     "from number missing",
			settings: notify.TwilioSettings{AccountSID: complete.AccountSID, AuthToken: complete.AuthToken},
		},
		{
			name:     "nothing set",
			settings: notify.TwilioSettings{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notifier := notify.FromSettings(tc.settings, nil)

			if _, ok := notifier.(*notify.LogNotifier); !ok {
				t.Fatalf("notifier = %T, want *notify.LogNotifier", notifier)
			}
		})
	}
}

func TestTwilioNotifierSendsConfiguredFromAndTo(t *testing.T) {
	sender := &fakeTwilioSender{}
	notifier := notify.NewTwilioNotifier(sender, "+15005550006", nil)

	msg := notify.Message{To: "+14155550123", Body: "Checked in to Central Plaza."}
	if err := notifier.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	if sender.params == nil {
		t.Fatal("no message params reached the twilio client")
	}
	if got := stringValue(sender.params.To); got != msg.To {
		t.Errorf("to = %q, want %q", got, msg.To)
	}
	if got := stringValue(sender.params.From); got != "+15005550006" {
		t.Errorf("from = %q, want %q", got, "+15005550006")
	}
	if got := stringValue(sender.params.Body); got != msg.Body {
		t.Errorf("body = %q, want %q", got, msg.Body)
	}
}

func TestTwilioNotifierReturnsProviderError(t *testing.T) {
	providerErr := errors.New("twilio rejected the message")
	sender := &fakeTwilioSender{err: providerErr}
	notifier := notify.NewTwilioNotifier(sender, "+15005550006", nil)

	err := notifier.Send(context.Background(), notify.Message{To: "+14155550123", Body: "Checked in."})
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want the provider error", err)
	}
	if err != providerErr {
		t.Errorf("error = %v, want the provider error unchanged", err)
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
