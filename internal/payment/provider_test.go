package payment_test

import (
	"context"
	"testing"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/payment"
)

func TestFakeProviderCreatesPaymentWithoutClientSecret(t *testing.T) {
	provider := payment.NewFakeProvider(nil)

	result, err := provider.CreatePayment(context.Background(), payment.CreateRequest{
		PaymentID: "pay_1",
		SessionID: "ses_1",
		LotID:     "lot_1",
		Amount:    5000,
		Currency:  domain.CurrencyINR,
	})
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if result.ClientSecret != "" {
		t.Errorf("client secret = %q, want none from the fake provider", result.ClientSecret)
	}
}

func TestFakeProviderParsesWebhook(t *testing.T) {
	provider := payment.NewFakeProvider(nil)

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
