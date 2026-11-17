package notify

import (
	"context"
	"log/slog"
	"strings"

	"github.com/twilio/twilio-go"
	api "github.com/twilio/twilio-go/rest/api/v2010"
)

// TwilioSettings are the three values the SMS provider needs. They come from the
// environment and are never logged.
type TwilioSettings struct {
	AccountSID string
	AuthToken  string
	FromNumber string
}

// Configured reports whether every value is present. A single missing setting
// leaves the service on the log-only notifier.
func (s TwilioSettings) Configured() bool {
	return strings.TrimSpace(s.AccountSID) != "" &&
		strings.TrimSpace(s.AuthToken) != "" &&
		strings.TrimSpace(s.FromNumber) != ""
}

// FromSettings returns the Twilio notifier when every setting is present and a
// log-only notifier otherwise, so local development and tests need no account.
// A client that cannot be built falls back the same way.
func FromSettings(settings TwilioSettings, logger *slog.Logger) Notifier {
	if logger == nil {
		logger = slog.Default()
	}
	if !settings.Configured() {
		return NewLogNotifier(logger)
	}

	restClient := twilio.NewRestClientWithParams(twilio.ClientParams{
		Username: strings.TrimSpace(settings.AccountSID),
		Password: strings.TrimSpace(settings.AuthToken),
	})

	return NewTwilioNotifier(apiSender{rest: restClient}, strings.TrimSpace(settings.FromNumber), logger)
}

// twilioSender is the narrow slice of the official twilio-go api client the
// notifier needs. It lives in this package so tests inject a fake and never
// touch the network; the generated response type differs between twilio-go
// versions, so the seam carries only the error the caller logs.
type twilioSender interface {
	CreateMessageWithContext(ctx context.Context, params *api.CreateMessageParams) error
}

// apiSender binds the generated api client to the twilioSender seam. A nil
// error already means Twilio accepted the message, so the response body is not
// inspected.
type apiSender struct {
	rest *twilio.RestClient
}

func (s apiSender) CreateMessageWithContext(ctx context.Context, params *api.CreateMessageParams) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.rest.Api.CreateMessage(params)
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TwilioNotifier sends the confirmation through the Twilio REST API.
type TwilioNotifier struct {
	client twilioSender
	from   string
	logger *slog.Logger
}

// NewTwilioNotifier returns a notifier that sends through client from the
// configured sender number. A nil logger falls back to slog.Default().
func NewTwilioNotifier(client twilioSender, from string, logger *slog.Logger) *TwilioNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &TwilioNotifier{client: client, from: from, logger: logger}
}

// Send makes exactly one attempt, bounded by ctx, and returns the provider or
// transport error unchanged so the caller can log it against the session.
func (n *TwilioNotifier) Send(ctx context.Context, msg Message) error {
	params := &api.CreateMessageParams{}
	params.SetTo(msg.To)
	params.SetFrom(n.from)
	params.SetBody(msg.Body)

	if err := n.client.CreateMessageWithContext(ctx, params); err != nil {
		return err
	}
	n.logger.Info("confirmation message sent", "to", msg.To)
	return nil
}
