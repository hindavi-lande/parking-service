// Package payments selects the payment provider the service runs with. It is
// the only package that knows which providers exist, so internal/service and
// internal/domain keep depending on payment.Provider alone.
package payments

import (
	"fmt"
	"strings"

	"github.com/techverito/parking-service/internal/payment"
	"github.com/techverito/parking-service/internal/payments/razorpay"
	"github.com/techverito/parking-service/internal/payments/stripe"
)

// Settings are the configuration values provider selection needs. The Stripe
// and Razorpay secrets are credentials: they are never logged.
type Settings struct {
	Name                string
	StripeSecretKey     string
	StripeWebhookSecret string
	StripeBaseURL       string

	// RazorpayKeyID is the publishable key id the client launches Razorpay
	// Checkout with, so it is not a secret. RazorpayKeySecret and
	// RazorpayWebhookSecret are credentials. RazorpayBaseURL is optional; empty
	// means the Razorpay API default.
	RazorpayKeyID         string
	RazorpayKeySecret     string
	RazorpayWebhookSecret string
	RazorpayBaseURL       string
}

// FromSettings returns the provider Settings.Name selects. The name is compared
// case-insensitively with surrounding space trimmed; an unknown name is an
// error so startup can fail loudly instead of running without a provider.
func FromSettings(settings Settings) (payment.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(settings.Name)) {
	case payment.FakeProviderName:
		return payment.NewFakeProvider(nil), nil
	case payment.StripeProviderName:
		return stripe.NewProvider(
			settings.StripeSecretKey,
			settings.StripeWebhookSecret,
			settings.StripeBaseURL,
			nil,
		), nil
	case payment.RazorpayProviderName:
		return razorpay.NewProvider(
			settings.RazorpayKeyID,
			settings.RazorpayKeySecret,
			settings.RazorpayWebhookSecret,
			settings.RazorpayBaseURL,
			nil,
		), nil
	default:
		return nil, fmt.Errorf("unknown payment provider %q", settings.Name)
	}
}
