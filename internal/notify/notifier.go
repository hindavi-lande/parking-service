// Package notify sends short messages to drivers. It owns the seam between the
// business code and a messaging provider, so the service never talks to Twilio
// directly and local development needs no account.
package notify

import "context"

// Message is one short message to a driver.
type Message struct {
	// To is the recipient in E.164 format, for example +14155552671.
	To string
	// Body is the composed SMS text, at most 160 characters.
	Body string
}

// Notifier sends a message to one recipient. Implementations must be safe for
// concurrent use, and must return promptly once the context deadline expires.
type Notifier interface {
	Send(ctx context.Context, message Message) error
}
