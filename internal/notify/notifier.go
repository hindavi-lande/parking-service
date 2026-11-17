// Package notify sends the confirmation messages the service produces. It has
// no knowledge of HTTP or storage; the provider stays behind the Notifier
// interface so local development runs on a log-only implementation.
package notify

import (
	"context"
	"log/slog"
)

// Message is one outbound SMS. To is the E.164 recipient and Body is the
// composed text. No credential is ever part of a message.
type Message struct {
	To   string
	Body string
}

// Notifier sends one message. It is the only seam the services depend on, so a
// log-only implementation can stand in for the provider.
type Notifier interface {
	Send(ctx context.Context, msg Message) error
}

// LogNotifier writes the message to the log instead of sending it, so local
// development and tests need no account.
type LogNotifier struct {
	logger *slog.Logger
}

// NewLogNotifier returns a log-only notifier. A nil logger falls back to
// slog.Default().
func NewLogNotifier(logger *slog.Logger) *LogNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogNotifier{logger: logger}
}

// Send logs the recipient and the body and returns nil. It never performs I/O.
func (n *LogNotifier) Send(_ context.Context, msg Message) error {
	n.logger.Info("confirmation message not sent: no SMS provider configured",
		"to", msg.To,
		"body", msg.Body,
	)
	return nil
}
