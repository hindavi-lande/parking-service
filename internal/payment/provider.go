// Package payment is the only place a payment provider is known. It keeps the
// provider behind an interface so internal/service and internal/domain never
// import a provider SDK or an HTTP client.
package payment

import (
	"context"
	"fmt"
	"strings"

	"github.com/techverito/parking-service/internal/domain"
)

// FakeProviderName is the provider PAYMENT_PROVIDER selects by default. It
// needs no account, so the service and its tests run without one.
const FakeProviderName = "fake"

// CreateRequest is everything a provider needs to open a payment. Amount is an
// integer in minor units of Currency.
type CreateRequest struct {
	PaymentID string
	SessionID string
	LotID     string
	Amount    int64
	Currency  string
}

// WebhookRequest is a raw provider callback. Body is the untouched request body
// and Header carries lower-cased header names, so a provider can verify a
// signature without the service importing net/http.
type WebhookRequest struct {
	Body   []byte
	Header map[string]string
}

// WebhookEvent is the part of a callback the service acts on. Only PAID and
// FAILED are meaningful statuses.
type WebhookEvent struct {
	PaymentID string
	Status    domain.PaymentStatus
}

// Provider opens a payment and reports what a callback means.
type Provider interface {
	Name() string
	CreatePayment(ctx context.Context, req CreateRequest) error
	ParseWebhook(ctx context.Context, req WebhookRequest) (WebhookEvent, error)
}

// FromName returns the provider registered under name. Only the fake provider
// exists today, so any other name is an error and startup can fail loudly
// instead of silently running without a provider.
func FromName(name string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case FakeProviderName:
		return NewFakeProvider(nil), nil
	default:
		return nil, fmt.Errorf("unknown payment provider %q", name)
	}
}
