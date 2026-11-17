package notify

import (
	"context"
	"log/slog"

	"github.com/techverito/parking-service/internal/domain"
)

// LogNotifier writes the confirmation message to the log instead of sending it,
// which is what keeps local development and tests free of a Twilio account.
type LogNotifier struct {
	logger *slog.Logger
}

// NewLogNotifier builds the notifier used when Twilio is not configured.
func NewLogNotifier(logger *slog.Logger) *LogNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogNotifier{logger: logger}
}

// NotifyCheckIn writes the message the driver would have received, so the body
// can be checked without a provider. It always succeeds.
func (n *LogNotifier) NotifyCheckIn(_ context.Context, notice domain.CheckInNotice) error {
	n.logger.Info("check-in confirmation not sent, twilio is not configured",
		"to", notice.Phone,
		"session_id", notice.SessionID,
		"body", BuildBody(notice),
	)
	return nil
}
