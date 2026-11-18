// Package stripe is the only place the Stripe API is known. It implements
// payment.Provider, so internal/service and internal/domain keep depending on
// the interface and never see the secret key or an HTTP client.
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/payment"
)

const (
	// defaultBaseURL is the Stripe API endpoint used when no base URL is
	// configured.
	defaultBaseURL = "https://api.stripe.com"
	// requestTimeout bounds a single Stripe call when no client is injected.
	requestTimeout = 10 * time.Second
	// signatureTolerance is how far a Stripe-Signature timestamp may be from
	// the current time before the callback is rejected.
	signatureTolerance = 5 * time.Minute
	// maxResponseBytes caps how much of a Stripe response is read.
	maxResponseBytes = 1 << 20
)

// Provider is the Stripe payment.Provider. secretKey and webhookSecret are
// credentials: they are never logged and never appear in an error message.
type Provider struct {
	secretKey     string
	webhookSecret string
	baseURL       string
	httpClient    *http.Client
}

// NewProvider returns a Stripe provider. An empty baseURL falls back to the
// Stripe API and a nil httpClient to a client with an explicit timeout, so a
// hanging call cannot keep a checkout open forever.
func NewProvider(secretKey, webhookSecret, baseURL string, httpClient *http.Client) *Provider {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Provider{
		secretKey:     strings.TrimSpace(secretKey),
		webhookSecret: strings.TrimSpace(webhookSecret),
		baseURL:       baseURL,
		httpClient:    httpClient,
	}
}

// Name returns the provider name PAYMENT_PROVIDER selects.
func (p *Provider) Name() string {
	return payment.StripeProviderName
}

// CreatePayment opens a PaymentIntent for the amount and currency in req. The
// amount is already in minor units, which is what Stripe expects, so it passes
// through unchanged. The card data never reaches this service: the client
// confirms the PaymentIntent itself with the returned client secret. A transport
// error, a timeout or a 5xx is wrapped with domain.ErrProviderUnavailable so the
// caller can report that the payment is still PENDING.
func (p *Provider) CreatePayment(ctx context.Context, req payment.CreateRequest) (payment.CreateResult, error) {
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(req.Amount, 10))
	form.Set("currency", req.Currency)
	form.Set("metadata[sessionId]", req.SessionID)
	form.Set("metadata[paymentId]", req.PaymentID)

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/payment_intents", strings.NewReader(form.Encode()))
	if err != nil {
		return payment.CreateResult{}, fmt.Errorf("build stripe payment intent request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+p.secretKey)
	httpRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := p.httpClient.Do(httpRequest)
	if err != nil {
		return payment.CreateResult{}, fmt.Errorf("create stripe payment intent: %w: %w", err, domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return payment.CreateResult{}, fmt.Errorf("read stripe payment intent response: %w: %w", err, domain.ErrProviderUnavailable)
	}

	if response.StatusCode >= http.StatusInternalServerError {
		return payment.CreateResult{}, fmt.Errorf("create stripe payment intent: stripe answered %d: %s: %w", response.StatusCode, stripeErrorMessage(body), domain.ErrProviderUnavailable)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return payment.CreateResult{}, fmt.Errorf("create stripe payment intent: stripe answered %d: %s", response.StatusCode, stripeErrorMessage(body))
	}

	var intent paymentIntentResponse
	if err := json.Unmarshal(body, &intent); err != nil {
		return payment.CreateResult{}, fmt.Errorf("decode stripe payment intent: %w", err)
	}
	return payment.CreateResult{ClientSecret: intent.ClientSecret}, nil
}

// ParseWebhook verifies the Stripe-Signature header against the configured
// webhook secret and maps a supported event to a status change. A callback that
// does not verify is a validation error, so the HTTP layer answers 400 and no
// payment changes.
func (p *Provider) ParseWebhook(_ context.Context, req payment.WebhookRequest) (payment.WebhookEvent, error) {
	if err := VerifySignature(req.Body, req.Header["stripe-signature"], p.webhookSecret, signatureTolerance, time.Now()); err != nil {
		return payment.WebhookEvent{}, &domain.ValidationError{Field: "stripe-signature", Message: err.Error()}
	}

	var event stripeEvent
	if err := json.Unmarshal(req.Body, &event); err != nil {
		return payment.WebhookEvent{}, &domain.ValidationError{
			Field:   "body",
			Message: fmt.Sprintf("is not a valid stripe event: %v", err),
		}
	}

	switch event.Type {
	case "payment_intent.succeeded":
		return payment.WebhookEvent{
			PaymentID: event.Data.Object.Metadata.PaymentID,
			Status:    domain.PaymentPaid,
			EventID:   event.ID,
		}, nil
	case "payment_intent.payment_failed":
		return payment.WebhookEvent{
			PaymentID:      event.Data.Object.Metadata.PaymentID,
			Status:         domain.PaymentFailed,
			EventID:        event.ID,
			FailureMessage: event.Data.Object.LastPaymentError.Message,
		}, nil
	default:
		return payment.WebhookEvent{}, &domain.ValidationError{
			Field:   "type",
			Message: fmt.Sprintf("must be payment_intent.succeeded or payment_intent.payment_failed, got %q", event.Type),
		}
	}
}

// VerifySignature checks a Stripe-Signature header against payload and secret.
// The header carries a timestamp and one or more v1 signatures, each an
// HMAC-SHA256 over "<timestamp>.<payload>". The signature is accepted only when
// a v1 value matches in constant time and the timestamp is within tolerance of
// now; a non-positive tolerance skips the window check.
func VerifySignature(payload []byte, header, secret string, tolerance time.Duration, now time.Time) error {
	if strings.TrimSpace(header) == "" {
		return errors.New("is required")
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("cannot be verified: no webhook secret is configured")
	}

	timestamp := int64(-1)
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		switch key {
		case "t":
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return errors.New("has an unreadable timestamp")
			}
			timestamp = parsed
		case "v1":
			signatures = append(signatures, value)
		}
	}
	if timestamp < 0 {
		return errors.New("has no timestamp")
	}
	if len(signatures) == 0 {
		return errors.New("has no v1 signature")
	}
	if tolerance > 0 {
		drift := now.Sub(time.Unix(timestamp, 0))
		if drift > tolerance || drift < -tolerance {
			return errors.New("timestamp is outside the tolerance window")
		}
	}

	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	for _, candidate := range signatures {
		if hmac.Equal([]byte(expected), []byte(candidate)) {
			return nil
		}
	}
	return errors.New("does not match the configured secret")
}

// paymentIntentResponse is the part of a Stripe PaymentIntent the service
// stores: the single-use client secret the client confirms with.
type paymentIntentResponse struct {
	ID           string `json:"id"`
	ClientSecret string `json:"client_secret"`
}

// stripeEvent is the part of a Stripe event the service acts on.
type stripeEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object struct {
			Metadata struct {
				SessionID string `json:"sessionId"`
				PaymentID string `json:"paymentId"`
			} `json:"metadata"`
			LastPaymentError struct {
				Message string `json:"message"`
			} `json:"last_payment_error"`
		} `json:"object"`
	} `json:"data"`
}

// stripeErrorMessage reads the message out of a Stripe error body without
// echoing the request or a credential.
func stripeErrorMessage(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Message != "" {
		return payload.Error.Message
	}
	return "unexpected response"
}
