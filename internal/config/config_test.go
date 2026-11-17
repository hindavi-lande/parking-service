package config_test

import (
	"testing"

	"github.com/techverito/parking-service/internal/config"
)

func TestLoadReadsTwilioSettings(t *testing.T) {
	t.Setenv("TWILIO_ACCOUNT_SID", "AC00000000000000000000000000000000")
	t.Setenv("TWILIO_AUTH_TOKEN", "test-auth-token")
	t.Setenv("TWILIO_FROM_NUMBER", "+14155552671")

	cfg := config.Load()

	if cfg.TwilioAccountSID != "AC00000000000000000000000000000000" {
		t.Errorf("TwilioAccountSID = %q, want the value from TWILIO_ACCOUNT_SID", cfg.TwilioAccountSID)
	}
	if cfg.TwilioAuthToken != "test-auth-token" {
		t.Errorf("TwilioAuthToken = %q, want the value from TWILIO_AUTH_TOKEN", cfg.TwilioAuthToken)
	}
	if cfg.TwilioFromNumber != "+14155552671" {
		t.Errorf("TwilioFromNumber = %q, want the value from TWILIO_FROM_NUMBER", cfg.TwilioFromNumber)
	}
}

func TestLoadDefaultsTwilioToEmpty(t *testing.T) {
	t.Setenv("TWILIO_ACCOUNT_SID", "")
	t.Setenv("TWILIO_AUTH_TOKEN", "")
	t.Setenv("TWILIO_FROM_NUMBER", "")

	cfg := config.Load()

	if cfg.TwilioAccountSID != "" || cfg.TwilioAuthToken != "" || cfg.TwilioFromNumber != "" {
		t.Errorf("twilio settings = %q/%q/%q, want all empty", cfg.TwilioAccountSID, cfg.TwilioAuthToken, cfg.TwilioFromNumber)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want the existing default :8080", cfg.Addr)
	}
}
