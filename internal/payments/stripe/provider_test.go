package stripe_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/payment"
	"github.com/techverito/parking-service/internal/payments/stripe"
)

const (
	testSecretKey     = "sk_test_key"
	testWebhookSecret = "whsec_test_secret"
)

// newProvider points a provider at the stub API and closes nothing: the caller
// owns the stub server.
func newProvider(stub *httptest.Server) *stripe.Provider {
	return stripe.NewProvider(testSecretKey, testWebhookSecret, stub.URL, stub.Client())
}

func createRequest() payment.CreateRequest {
	return payment.CreateRequest{
		PaymentID: "pay_1",
		SessionID: "ses_1",
		LotID:     "lot_1",
		Amount:    5000,
		Currency:  domain.CurrencyINR,
	}
}

// sign returns the header the provider verifies: t=<unix>,v1=<hex hmac over
// "<timestamp>.<payload>">.
func sign(secret string, payload []byte, timestamp time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp.Unix())
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", timestamp.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

// webhookRequest signs body with the configured secret, so a test only states
// the event it wants to deliver.
func webhookRequest(body []byte) payment.WebhookRequest {
	return payment.WebhookRequest{
		Body:   body,
		Header: map[string]string{"stripe-signature": sign(testWebhookSecret, body, time.Now())},
	}
}

func TestNameIsStripe(t *testing.T) {
	provider := stripe.NewProvider(testSecretKey, testWebhookSecret, "", nil)

	if got := provider.Name(); got != payment.StripeProviderName {
		t.Errorf("name = %q, want %q", got, payment.StripeProviderName)
	}
}

func TestCreatePaymentSendsPaymentIntentAndReturnsClientSecret(t *testing.T) {
	var received url.Values
	var method, path, authorization string

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		authorization = r.Header.Get("Authorization")
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		received = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"pi_1","client_secret":"cs_test_1"}`)
	}))
	defer stub.Close()

	provider := newProvider(stub)

	result, err := provider.CreatePayment(context.Background(), createRequest())
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if result.ClientSecret != "cs_test_1" {
		t.Errorf("client secret = %q, want cs_test_1", result.ClientSecret)
	}
	if method != http.MethodPost || path != "/v1/payment_intents" {
		t.Errorf("request = %s %s, want POST /v1/payment_intents", method, path)
	}
	if authorization != "Bearer "+testSecretKey {
		t.Errorf("Authorization = %q, want the bearer secret key", authorization)
	}
	if got := received.Get("amount"); got != "5000" {
		t.Errorf("amount = %q, want 5000", got)
	}
	if got := received.Get("currency"); got != domain.CurrencyINR {
		t.Errorf("currency = %q, want %q", got, domain.CurrencyINR)
	}
	if got := received.Get("metadata[sessionId]"); got != "ses_1" {
		t.Errorf("metadata[sessionId] = %q, want ses_1", got)
	}
	if got := received.Get("metadata[paymentId]"); got != "pay_1" {
		t.Errorf("metadata[paymentId] = %q, want pay_1", got)
	}

	metadataKeys := 0
	for key := range received {
		if strings.HasPrefix(key, "metadata[") {
			metadataKeys++
		}
	}
	if metadataKeys != 2 {
		t.Errorf("metadata keys = %d in %v, want exactly sessionId and paymentId", metadataKeys, received)
	}
}

func TestCreatePaymentWrapsServerErrorAsProviderUnavailable(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"stripe is down"}}`)
	}))
	defer stub.Close()

	provider := newProvider(stub)

	if _, err := provider.CreatePayment(context.Background(), createRequest()); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
}

func TestCreatePaymentWrapsTransportErrorAsProviderUnavailable(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	provider := stripe.NewProvider(testSecretKey, testWebhookSecret, stub.URL, stub.Client())
	stub.Close()

	if _, err := provider.CreatePayment(context.Background(), createRequest()); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
}

func TestCreatePaymentWrapsExpiredContextAsProviderUnavailable(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer stub.Close()

	provider := newProvider(stub)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if _, err := provider.CreatePayment(ctx, createRequest()); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
}

func TestParseWebhookMapsSucceededEvent(t *testing.T) {
	provider := stripe.NewProvider(testSecretKey, testWebhookSecret, "", nil)
	body := []byte(`{"id":"evt_1","type":"payment_intent.succeeded","data":{"object":{"id":"pi_1","metadata":{"sessionId":"ses_1","paymentId":"pay_1"}}}}`)

	event, err := provider.ParseWebhook(context.Background(), webhookRequest(body))
	if err != nil {
		t.Fatalf("parse webhook: %v", err)
	}
	if event.EventID != "evt_1" {
		t.Errorf("event id = %q, want evt_1", event.EventID)
	}
	if event.PaymentID != "pay_1" {
		t.Errorf("payment id = %q, want pay_1", event.PaymentID)
	}
	if event.Status != domain.PaymentPaid {
		t.Errorf("status = %q, want %q", event.Status, domain.PaymentPaid)
	}
	if event.FailureMessage != "" {
		t.Errorf("failure message = %q, want none for a succeeded payment", event.FailureMessage)
	}
}

func TestParseWebhookMapsFailedEventWithFailureMessage(t *testing.T) {
	provider := stripe.NewProvider(testSecretKey, testWebhookSecret, "", nil)
	body := []byte(`{"id":"evt_2","type":"payment_intent.payment_failed","data":{"object":{"id":"pi_1","metadata":{"sessionId":"ses_1","paymentId":"pay_1"},"last_payment_error":{"message":"Your card was declined."}}}}`)

	event, err := provider.ParseWebhook(context.Background(), webhookRequest(body))
	if err != nil {
		t.Fatalf("parse webhook: %v", err)
	}
	if event.EventID != "evt_2" {
		t.Errorf("event id = %q, want evt_2", event.EventID)
	}
	if event.PaymentID != "pay_1" {
		t.Errorf("payment id = %q, want pay_1", event.PaymentID)
	}
	if event.Status != domain.PaymentFailed {
		t.Errorf("status = %q, want %q", event.Status, domain.PaymentFailed)
	}
	if event.FailureMessage != "Your card was declined." {
		t.Errorf("failure message = %q, want the stripe message", event.FailureMessage)
	}
}

func TestParseWebhookRejectsBadSignatures(t *testing.T) {
	provider := stripe.NewProvider(testSecretKey, testWebhookSecret, "", nil)
	body := []byte(`{"id":"evt_3","type":"payment_intent.succeeded","data":{"object":{"metadata":{"paymentId":"pay_1"}}}}`)

	cases := []struct {
		name   string
		header string
	}{
		{name: "missing header", header: ""},
		{name: "wrong secret", header: sign("whsec_someone_else", body, time.Now())},
		{name: "stale timestamp", header: sign(testWebhookSecret, body, time.Now().Add(-time.Hour))},
		{name: "tampered body", header: sign(testWebhookSecret, []byte(`{"id":"evt_other"}`), time.Now())},
		{name: "no v1 scheme", header: fmt.Sprintf("t=%d", time.Now().Unix())},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := provider.ParseWebhook(context.Background(), payment.WebhookRequest{
				Body:   body,
				Header: map[string]string{"stripe-signature": tc.header},
			})
			if err == nil {
				t.Fatal("expected a signature error")
			}

			// A validation error is what makes the HTTP layer answer 400.
			var validation *domain.ValidationError
			if !errors.As(err, &validation) {
				t.Errorf("error = %v, want a ValidationError", err)
			}
		})
	}
}

func TestParseWebhookRejectsUnsupportedEventType(t *testing.T) {
	provider := stripe.NewProvider(testSecretKey, testWebhookSecret, "", nil)
	body := []byte(`{"id":"evt_4","type":"charge.refunded","data":{"object":{"metadata":{"paymentId":"pay_1"}}}}`)

	if _, err := provider.ParseWebhook(context.Background(), webhookRequest(body)); err == nil {
		t.Fatal("expected an error for an unsupported event type")
	}
}

func TestVerifySignatureAcceptsAValidHeader(t *testing.T) {
	payload := []byte(`{"id":"evt_5"}`)
	now := time.Now()

	if err := stripe.VerifySignature(payload, sign(testWebhookSecret, payload, now), testWebhookSecret, 5*time.Minute, now); err != nil {
		t.Errorf("verify signature: %v", err)
	}
}
