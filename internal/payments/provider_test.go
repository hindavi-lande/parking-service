package payments_test

import (
	"strings"
	"testing"

	"github.com/techverito/parking-service/internal/payment"
	"github.com/techverito/parking-service/internal/payments"
)

func TestFromNameSelectsTheFakeProvider(t *testing.T) {
	provider, err := payments.FromSettings(payments.Settings{Name: "fake"})
	if err != nil {
		t.Fatalf("from settings: %v", err)
	}
	if got := provider.Name(); got != payment.FakeProviderName {
		t.Errorf("name = %q, want %q", got, payment.FakeProviderName)
	}
}

func TestFromNameRejectsUnknownProvider(t *testing.T) {
	provider, err := payments.FromSettings(payments.Settings{Name: "paypal"})
	if err == nil {
		t.Fatalf("provider = %v, want an error for an unknown provider", provider)
	}
	if !strings.Contains(err.Error(), "paypal") {
		t.Errorf("error = %v, want it to name the unknown provider", err)
	}
}

func TestFromSettingsSelectsStripe(t *testing.T) {
	provider, err := payments.FromSettings(payments.Settings{
		Name:                "  STRIPE ",
		StripeSecretKey:     "sk_test_config",
		StripeWebhookSecret: "whsec_config",
		StripeBaseURL:       "http://127.0.0.1:12111",
	})
	if err != nil {
		t.Fatalf("from settings: %v", err)
	}
	if got := provider.Name(); got != payment.StripeProviderName {
		t.Errorf("name = %q, want %q", got, payment.StripeProviderName)
	}
}
