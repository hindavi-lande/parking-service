// Package razorpay is the only place the Razorpay API is known. It implements
// payment.Provider, so internal/service and internal/domain keep depending on
// the interface and never see the key secret or an HTTP client.
package razorpay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/payment"
)

const (
	// defaultBaseURL is the Razorpay API endpoint used when no base URL is
	// configured.
	defaultBaseURL = "https://api.razorpay.com"
	// requestTimeout bounds a single Razorpay call when no client is injected.
	requestTimeout = 10 * time.Second
	// maxResponseBytes caps how much of a Razorpay response is read.
	maxResponseBytes = 1 << 20
	// signatureHeader is the lower-cased header Razorpay signs a callback body
	// with. WebhookRequest.Header carries lower-cased names.
	signatureHeader = "x-razorpay-signature"
	// eventIDHeader is the lower-cased header carrying the provider's own event
	// id, which is what makes a repeated callback recognisable.
	eventIDHeader = "x-razorpay-event-id"
)

// Provider is the Razorpay payment.Provider. keySecret and webhookSecret are
// credentials: they are never logged and never appear in an error message.
// keyID is the publishable key id Razorpay Checkout launches with.
type Provider struct {
	keyID         string
	keySecret     string
	webhookSecret string
	baseURL       string
	httpClient    *http.Client
}

// NewProvider returns a Razorpay provider. An empty baseURL falls back to the
// Razorpay API and a nil httpClient to a client with an explicit timeout, so a
// hanging call cannot keep a checkout open forever.
func NewProvider(keyID, keySecret, webhookSecret, baseURL string, httpClient *http.Client) *Provider {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Provider{
		keyID:         strings.TrimSpace(keyID),
		keySecret:     strings.TrimSpace(keySecret),
		webhookSecret: strings.TrimSpace(webhookSecret),
		baseURL:       baseURL,
		httpClient:    httpClient,
	}
}

// Name returns the provider name PAYMENT_PROVIDER selects.
func (p *Provider) Name() string {
	return payment.RazorpayProviderName
}

// orderRequest is the body of a Razorpay order creation. Amount is already in
// paise, which are minor units, so it passes through as an integer.
type orderRequest struct {
	Amount   int64             `json:"amount"`
	Currency string            `json:"currency"`
	Receipt  string            `json:"receipt"`
	Notes    map[string]string `json:"notes"`
}

// orderResponse is the part of a Razorpay order the service needs: its id.
type orderResponse struct {
	ID       string `json:"id"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Receipt  string `json:"receipt"`
}

// CreatePayment opens a Razorpay order for the amount and currency in req. The
// amount is already in paise, which is what Razorpay expects, so it passes
// through unchanged and no float conversion happens. The card data never
// reaches this service: the client launches Razorpay Checkout with the returned
// order id and publishable key id. A transport error, a timeout or a 5xx is
// wrapped with domain.ErrProviderUnavailable so the caller can report that the
// payment is still PENDING.
func (p *Provider) CreatePayment(ctx context.Context, req payment.CreateRequest) (payment.CreateResult, error) {
	body, err := json.Marshal(orderRequest{
		Amount:   req.Amount,
		Currency: req.Currency,
		Receipt:  req.PaymentID,
		Notes:    map[string]string{"sessionId": req.SessionID},
	})
	if err != nil {
		return payment.CreateResult{}, fmt.Errorf("encode razorpay order request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/orders", bytes.NewReader(body))
	if err != nil {
		return payment.CreateResult{}, fmt.Errorf("build razorpay order request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.keyID+":"+p.keySecret)))
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := p.httpClient.Do(httpRequest)
	if err != nil {
		return payment.CreateResult{}, fmt.Errorf("create razorpay order: %w: %w", err, domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return payment.CreateResult{}, fmt.Errorf("read razorpay order response: %w: %w", err, domain.ErrProviderUnavailable)
	}

	if response.StatusCode >= http.StatusInternalServerError {
		return payment.CreateResult{}, fmt.Errorf("create razorpay order: razorpay answered %d: %s: %w", response.StatusCode, razorpayErrorMessage(responseBody), domain.ErrProviderUnavailable)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return payment.CreateResult{}, fmt.Errorf("create razorpay order: razorpay answered %d: %s", response.StatusCode, razorpayErrorMessage(responseBody))
	}

	var order orderResponse
	if err := json.Unmarshal(responseBody, &order); err != nil {
		return payment.CreateResult{}, fmt.Errorf("decode razorpay order: %w", err)
	}
	if order.ID == "" {
		return payment.CreateResult{}, errors.New("decode razorpay order: response carries no order id")
	}
	return payment.CreateResult{OrderID: order.ID, KeyID: p.keyID}, nil
}

// webhookPayload is the part of a Razorpay event the service acts on.
type webhookPayload struct {
	Event   string `json:"event"`
	Payload struct {
		Payment struct {
			Entity struct {
				ID               string            `json:"id"`
				OrderID          string            `json:"order_id"`
				Amount           *int64            `json:"amount"`
				Currency         string            `json:"currency"`
				Status           string            `json:"status"`
				ErrorDescription string            `json:"error_description"`
				ErrorReason      string            `json:"error_reason"`
				ErrorCode        string            `json:"error_code"`
				Notes            map[string]string `json:"notes"`
			} `json:"entity"`
		} `json:"payment"`
	} `json:"payload"`
}

// ParseWebhook verifies the X-Razorpay-Signature header against the configured
// webhook secret and maps a supported event to a status change. A callback that
// does not verify is a validation error, so the HTTP layer answers 400 and no
// payment changes.
func (p *Provider) ParseWebhook(_ context.Context, req payment.WebhookRequest) (payment.WebhookEvent, error) {
	if err := VerifySignature(req.Body, req.Header[signatureHeader], p.webhookSecret); err != nil {
		return payment.WebhookEvent{}, &domain.ValidationError{Field: "x-razorpay-signature", Message: err.Error()}
	}

	var payload webhookPayload
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		return payment.WebhookEvent{}, &domain.ValidationError{
			Field:   "body",
			Message: fmt.Sprintf("is not a valid razorpay event: %v", err),
		}
	}

	entity := payload.Payload.Payment.Entity
	event := payment.WebhookEvent{
		PaymentID: entity.Notes["paymentId"],
		SessionID: entity.Notes["sessionId"],
		EventID:   req.Header[eventIDHeader],
		Amount:    entity.Amount,
		Currency:  entity.Currency,
	}

	switch payload.Event {
	case "payment.captured":
		event.Status = domain.PaymentPaid
		return event, nil
	case "payment.failed":
		event.Status = domain.PaymentFailed
		event.FailureMessage = razorpayFailureMessage(entity.ErrorDescription, entity.ErrorReason, entity.ErrorCode)
		return event, nil
	default:
		return payment.WebhookEvent{}, &domain.ValidationError{
			Field:   "event",
			Message: fmt.Sprintf("must be payment.captured or payment.failed, got %q", payload.Event),
		}
	}
}

// razorpayFailureMessage picks the most human-readable reason Razorpay sends.
// An empty result means the provider reported no reason.
func razorpayFailureMessage(description, reason, code string) string {
	for _, candidate := range []string{description, reason, code} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// VerifySignature checks a hex HMAC-SHA256 of payload against signature with
// secret. An empty signature or secret is an error, and a rejected signature
// never reveals the secret.
func VerifySignature(payload []byte, signature, secret string) error {
	if strings.TrimSpace(signature) == "" {
		return errors.New("is required")
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("cannot be verified: no webhook secret is configured")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signature))) {
		return errors.New("does not match the configured secret")
	}
	return nil
}

// razorpayErrorMessage reads the message out of a Razorpay error body without
// echoing the request or a credential.
func razorpayErrorMessage(body []byte) string {
	var payload struct {
		Error struct {
			Description string `json:"description"`
			Reason      string `json:"reason"`
			Code        string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		for _, candidate := range []string{payload.Error.Description, payload.Error.Reason, payload.Error.Code} {
			if trimmed := strings.TrimSpace(candidate); trimmed != "" {
				return trimmed
			}
		}
	}
	return "unexpected response"
}
