package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/techverito/parking-service/internal/domain"
)

// FakeProvider stands in for a real provider. It holds no credential and no
// client, and it never performs I/O.
type FakeProvider struct {
	logger *slog.Logger
}

// NewFakeProvider returns the default provider. A nil logger falls back to
// slog.Default().
func NewFakeProvider(logger *slog.Logger) *FakeProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &FakeProvider{logger: logger}
}

// Name returns the provider name PAYMENT_PROVIDER selects.
func (p *FakeProvider) Name() string {
	return FakeProviderName
}

// CreatePayment logs the payment and reports no client secret. It performs no
// I/O.
func (p *FakeProvider) CreatePayment(_ context.Context, req CreateRequest) (CreateResult, error) {
	p.logger.Info("fake payment provider accepted a payment",
		"payment_id", req.PaymentID,
		"session_id", req.SessionID,
		"lot_id", req.LotID,
		"amount", req.Amount,
		"currency", req.Currency,
	)
	return CreateResult{}, nil
}

// fakeWebhookPayload is the documented body the fake provider parses:
// {"paymentId":"pay_1","status":"PAID"}.
type fakeWebhookPayload struct {
	PaymentID string `json:"paymentId"`
	Status    string `json:"status"`
}

// ParseWebhook decodes the documented fake payload. A body that is not valid
// JSON, or a status that is neither PAID nor FAILED, is an error.
func (p *FakeProvider) ParseWebhook(_ context.Context, req WebhookRequest) (WebhookEvent, error) {
	var payload fakeWebhookPayload
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		return WebhookEvent{}, fmt.Errorf("decode fake webhook: %w", err)
	}

	status, err := webhookStatus(payload.Status)
	if err != nil {
		return WebhookEvent{}, err
	}
	return WebhookEvent{PaymentID: payload.PaymentID, Status: status}, nil
}

func webhookStatus(raw string) (domain.PaymentStatus, error) {
	switch status := domain.PaymentStatus(strings.ToUpper(strings.TrimSpace(raw))); status {
	case domain.PaymentPaid:
		return status, nil
	case domain.PaymentFailed:
		return status, nil
	default:
		return "", fmt.Errorf("fake webhook: unknown status %q", raw)
	}
}
