package payment_test

import (
	"context"
	"strings"
	"testing"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/payment"
)

func TestFromNameSelectsTheFakeProvider(t *testing.T) {
	provider, err := payment.FromName("fake")
	if err != nil {
		t.Fatalf("from name: %v", err)
	}
	if got := provider.Name(); got != payment.FakeProviderName {
		t.Errorf("name = %q, want %q", got, payment.FakeProviderName)
	}
}

func TestFromNameRejectsUnknownProvider(t *testing.T) {
	provider, err := payment.FromName("stripe")
	if err == nil {
		t.Fatalf("provider = %v, want an error for an unknown provider", provider)
	}
	if !strings.Contains(err.Error(), "stripe") {
		t.Errorf("error = %v, want it to name the unknown provider", err)
	}
}

func TestFakeProviderParsesWebhook(t *testing.T) {
	provider, err := payment.FromName(payment.FakeProviderName)
	if err != nil {
		t.Fatalf("from name: %v", err)
	}

	event, err := provider.ParseWebhook(context.Background(), payment.WebhookRequest{
		Body: []byte(`{"paymentId":"pay_1","status":"PAID"}`),
	})
	if err != nil {
		t.Fatalf("parse webhook: %v", err)
	}
	if event.PaymentID != "pay_1" {
		t.Errorf("payment id = %q, want pay_1", event.PaymentID)
	}
	if event.Status != domain.PaymentPaid {
		t.Errorf("status = %q, want %q", event.Status, domain.PaymentPaid)
	}

	if _, err := provider.ParseWebhook(context.Background(), payment.WebhookRequest{
		Body: []byte("not a json object"),
	}); err == nil {
		t.Fatal("expected an error for a malformed body")
	}
}
