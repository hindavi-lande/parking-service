package config_test

import (
	"testing"

	"github.com/techverito/parking-service/internal/config"
)

func TestLoadReadsTwilioSettings(t *testing.T) {
	t.Setenv("TWILIO_ACCOUNT_SID", "AC00000000000000000000000000000000")
	t.Setenv("TWILIO_AUTH_TOKEN", "auth-token")
	t.Setenv("TWILIO_FROM_NUMBER", "+14155552671")

	cfg := config.Load()

	if cfg.TwilioAccountSID != "AC00000000000000000000000000000000" {
		t.Errorf("account SID = %q, want the configured value", cfg.TwilioAccountSID)
	}
	if cfg.TwilioAuthToken != "auth-token" {
		t.Errorf("auth token = %q, want the configured value", cfg.TwilioAuthToken)
	}
	if cfg.TwilioFromNumber != "+14155552671" {
		t.Errorf("from number = %q, want the configured value", cfg.TwilioFromNumber)
	}
}

func TestLoadDefaultsTwilioSettingsToEmpty(t *testing.T) {
	t.Setenv("TWILIO_ACCOUNT_SID", "")
	t.Setenv("TWILIO_AUTH_TOKEN", "")
	t.Setenv("TWILIO_FROM_NUMBER", "")

	cfg := config.Load()

	if cfg.TwilioAccountSID != "" || cfg.TwilioAuthToken != "" || cfg.TwilioFromNumber != "" {
		t.Errorf("cfg = %+v, want the Twilio settings to default to empty", cfg)
	}
}

func TestTwilioConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{
			name: "no settings",
			cfg:  config.Config{},
			want: false,
		},
		{
			name: "missing auth token",
			cfg: config.Config{
				TwilioAccountSID: "AC00000000000000000000000000000000",
				TwilioFromNumber: "+14155552671",
			},
			want: false,
		},
		{
			name: "missing account SID",
			cfg: config.Config{
				TwilioAuthToken:  "auth-token",
				TwilioFromNumber: "+14155552671",
			},
			want: false,
		},
		{
			name: "missing from number",
			cfg: config.Config{
				TwilioAccountSID: "AC00000000000000000000000000000000",
				TwilioAuthToken:  "auth-token",
			},
			want: false,
		},
		{
			name: "all settings",
			cfg: config.Config{
				TwilioAccountSID: "AC00000000000000000000000000000000",
				TwilioAuthToken:  "auth-token",
				TwilioFromNumber: "+14155552671",
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.TwilioConfigured(); got != tc.want {
				t.Errorf("TwilioConfigured() = %v, want %v", got, tc.want)
			}
		})
	}
}
