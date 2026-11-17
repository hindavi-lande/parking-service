package notify

import (
	"log/slog"
	"strings"

	"github.com/techverito/parking-service/internal/service"
)

// Settings holds the Twilio configuration. The fields map to the
// TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN and TWILIO_FROM_NUMBER environment
// variables that internal/config reads.
type Settings struct {
	AccountSID string
	AuthToken  string
	FromNumber string
}

// Complete reports whether every setting needed to reach Twilio is present once
// surrounding space is trimmed.
func (s Settings) Complete() bool {
	return strings.TrimSpace(s.AccountSID) != "" &&
		strings.TrimSpace(s.AuthToken) != "" &&
		strings.TrimSpace(s.FromNumber) != ""
}

// New returns the notifier the service should use: Twilio when all three
// settings are present, the log-only notifier otherwise. An absent or partial
// configuration therefore never stops the service from starting, and local
// development and tests need no account. It cannot fail, so it has no error
// to return.
func New(settings Settings, logger *slog.Logger) service.Notifier {
	if logger == nil {
		logger = slog.Default()
	}
	if settings.Complete() {
		return NewTwilioNotifier(settings, logger)
	}
	logger.Warn("twilio is not configured, using the log-only notifier",
		"missing_settings", missingSettings(settings),
	)
	return NewLogNotifier(logger)
}

// missingSettings names the settings that are absent. Only the names are
// reported, never the values.
func missingSettings(settings Settings) []string {
	var missing []string
	if strings.TrimSpace(settings.AccountSID) == "" {
		missing = append(missing, "TWILIO_ACCOUNT_SID")
	}
	if strings.TrimSpace(settings.AuthToken) == "" {
		missing = append(missing, "TWILIO_AUTH_TOKEN")
	}
	if strings.TrimSpace(settings.FromNumber) == "" {
		missing = append(missing, "TWILIO_FROM_NUMBER")
	}
	return missing
}
