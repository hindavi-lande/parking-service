package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/twilio/twilio-go"
	api "github.com/twilio/twilio-go/rest/api/v2010"

	"github.com/techverito/parking-service/internal/domain"
)

// sendTimeout caps one send attempt. The ticket fixes it, so it is not read
// from the configuration.
const sendTimeout = 5 * time.Second

// Sender sends a single message. It exists so the timeout and the failure path
// can be tested without Twilio credentials.
type Sender interface {
	Send(ctx context.Context, to string, body string) error
}

// TwilioNotifier sends the check-in confirmation through Twilio. from is the
// number the message is sent from; the Sender performs the actual use of it.
type TwilioNotifier struct {
	sender Sender
	from   string
	logger *slog.Logger
}

// NewTwilioNotifier builds a notifier that sends through the official Twilio
// helper.
func NewTwilioNotifier(settings Settings, logger *slog.Logger) *TwilioNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	client := twilio.NewRestClientWithParams(twilio.ClientParams{
		Username: settings.AccountSID,
		Password: settings.AuthToken,
	})
	return NewTwilioNotifierWithSender(
		&twilioSender{client: client, from: settings.FromNumber},
		settings.FromNumber,
		logger,
	)
}

// NewTwilioNotifierWithSender builds a notifier around any Sender. Tests use it
// to exercise the timeout and failure paths without constructing a Twilio
// client.
func NewTwilioNotifierWithSender(sender Sender, from string, logger *slog.Logger) *TwilioNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &TwilioNotifier{sender: sender, from: from, logger: logger}
}

// NotifyCheckIn makes one best-effort attempt to send the confirmation. The
// error is returned untouched otherwise so the caller can log it; there is no
// retry, no queue and no dead-letter here.
func (n *TwilioNotifier) NotifyCheckIn(ctx context.Context, notice domain.CheckInNotice) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	if err := n.sender.Send(ctx, notice.Phone, BuildBody(notice)); err != nil {
		return fmt.Errorf("send check-in confirmation for session %q: %w", notice.SessionID, err)
	}
	return nil
}

// twilioSender sends through the official Twilio helper. It is the only place
// in the service that talks to Twilio.
type twilioSender struct {
	client *twilio.RestClient
	from   string
}

// Send hands one message to Twilio. The helper takes no context of its own, so
// the deadline the notifier sets can only be observed before the call starts.
func (s *twilioSender) Send(ctx context.Context, to string, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	params := &api.CreateMessageParams{}
	params.SetFrom(s.from)
	params.SetTo(to)
	params.SetBody(body)

	if _, err := s.client.Api.CreateMessage(params); err != nil {
		return fmt.Errorf("create twilio message: %w", err)
	}
	return nil
}
