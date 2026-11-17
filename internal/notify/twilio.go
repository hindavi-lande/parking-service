package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/twilio/twilio-go"
	api "github.com/twilio/twilio-go/rest/api/v2010"
)

// twilioTimeout bounds one send, so a provider that hangs cannot push the
// synchronous check-in past the HTTP server's request timeout.
const twilioTimeout = 5 * time.Second

// TwilioNotifier sends messages through the Twilio Messages API.
type TwilioNotifier struct {
	client     *twilio.RestClient
	from       string
	accountSID string
	timeout    time.Duration
}

var _ Notifier = (*TwilioNotifier)(nil)

// NewTwilioNotifier builds the Twilio REST client. It makes no network call, so
// startup does not depend on the provider being reachable. The auth token is
// handed to the client and never logged.
func NewTwilioNotifier(accountSID, authToken, fromNumber string, logger *slog.Logger) *TwilioNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Debug("twilio notifier configured", "from_number", fromNumber)

	return &TwilioNotifier{
		client: twilio.NewRestClientWithParams(twilio.ClientParams{
			Username: accountSID,
			Password: authToken,
		}),
		from:       fromNumber,
		accountSID: accountSID,
		timeout:    twilioTimeout,
	}
}

// Send asks Twilio to deliver one message. The wait is bounded by n.timeout and
// by the caller's context, so a hung provider cannot stall the check-in. A
// rejection or a timeout comes back as a wrapped error carrying no auth token
// and no account SID.
func (n *TwilioNotifier) Send(ctx context.Context, message Message) error {
	params := &api.CreateMessageParams{}
	params.SetTo(message.To)
	params.SetFrom(n.from)
	params.SetBody(message.Body)

	// The helper does not take a context, so the attempt runs on its own
	// goroutine and the caller stops waiting once the budget is spent.
	done := make(chan error, 1)
	go func() {
		_, err := n.client.Api.CreateMessage(params)
		done <- err
	}()

	timer := time.NewTimer(n.timeout)
	defer timer.Stop()

	select {
	case err := <-done:
		if err != nil {
			return n.wrapError("twilio rejected the message", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("send sms via twilio: %w", ctx.Err())
	case <-timer.C:
		return fmt.Errorf("send sms via twilio: timed out after %s", n.timeout)
	}
}

// wrapError keeps the provider detail an operator needs while removing the
// account SID, which Twilio puts into the request URL of a transport error.
func (n *TwilioNotifier) wrapError(summary string, err error) error {
	detail := err.Error()
	if n.accountSID != "" {
		detail = strings.ReplaceAll(detail, n.accountSID, "[redacted]")
	}
	return fmt.Errorf("send sms via twilio: %s: %s", summary, detail)
}
