package notify

import (
	"context"
	"log/slog"
)

// LogNotifier writes every message to the log instead of sending it. It is the
// default when no provider is configured, so local development and tests need
// no third-party account.
type LogNotifier struct {
	Logger *slog.Logger
}

var _ Notifier = (*LogNotifier)(nil)

// NewLogNotifier returns a log-only notifier, falling back to slog.Default()
// when no logger is supplied.
func NewLogNotifier(logger *slog.Logger) *LogNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogNotifier{Logger: logger}
}

// Send logs the recipient and the body at Info level so the message can be read
// locally. It sends no network traffic and always succeeds.
func (n *LogNotifier) Send(_ context.Context, message Message) error {
	logger := n.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("sms not sent, log-only notifier",
		"to", message.To,
		"body", message.Body,
	)
	return nil
}
