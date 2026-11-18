// Package payment is the only place a payment provider is known. It keeps the
// provider behind an interface so internal/service and internal/domain never
// import a provider SDK or an HTTP client.
package payment

import (
	"context"

	"github.com/techverito/parking-service/internal/domain"
)

const (
	// FakeProviderName is the provider PAYMENT_PROVIDER selects by default. It
	// needs no account, so the service and its tests run without one.
	FakeProviderName = "fake"
	// StripeProviderName is the provider PAYMENT_PROVIDER selects for Stripe. It
	// is declared here so internal/service can recognise the Stripe provider
	// without importing the client package.
	StripeProviderName = "stripe"
)

// CreateRequest is everything a provider needs to open a payment. Amount is an
// integer in minor units of Currency.
type CreateRequest struct {
	PaymentID string
	SessionID string
	LotID     string
	Amount    int64
	Currency  string
}

// CreateResult is what a provider reports after it opened a payment.
// ClientSecret is the single-use credential the client confirms the payment
// with; the zero value means the provider issues no client secret.
type CreateResult struct {
	ClientSecret string
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
	// EventID is the provider's own event identifier, used to handle a repeated
	// callback once. It is empty when the provider sends none.
	EventID string
	// FailureMessage is the provider's human-readable reason for a failure. It
	// is empty unless the event reports one.
	FailureMessage string
}

// Provider opens a payment and reports what a callback means.
type Provider interface {
	Name() string
	CreatePayment(ctx context.Context, req CreateRequest) (CreateResult, error)
	ParseWebhook(ctx context.Context, req WebhookRequest) (WebhookEvent, error)
}
