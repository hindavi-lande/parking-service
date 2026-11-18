package razorpay_test

import (
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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/payment"
	"github.com/techverito/parking-service/internal/payments/razorpay"
)

const (
	testKeyID         = "rzp_test_key_id"
	testKeySecret     = "rzp_test_key_secret"
	testWebhookSecret = "whsec_rzp_test_secret"
)

// newProvider points a provider at the stub API and closes nothing: the caller
// owns the stub server.
func newProvider(stub *httptest.Server) *razorpay.Provider {
	return razorpay.NewProvider(testKeyID, testKeySecret, testWebhookSecret, stub.URL, stub.Client())
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

// sign returns the hex HMAC-SHA256 of payload with secret, which is what the
// provider verifies.
func sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// webhookRequest signs body with the configured webhook secret and carries the
// given event id, so a test only states the event it wants to deliver.
func webhookRequest(body []byte, eventID string) payment.WebhookRequest {
	header := map[string]string{"x-razorpay-signature": sign(testWebhookSecret, body)}
	if eventID != "" {
		header["x-razorpay-event-id"] = eventID
	}
	return payment.WebhookRequest{Body: body, Header: header}
}

// paymentEntity renders a Razorpay event body with the given payment entity.
func paymentEntity(event, amount, currency, description, sessionID string) []byte {
	if amount == "" {
		return []byte(fmt.Sprintf(
			`{"event":%q,"payload":{"payment":{"entity":{"id":"pay_1","order_id":"order_1","currency":%q,"status":"captured","error_description":%q,"notes":{"paymentId":"pay_1","sessionId":%q}}}}}`,
			event, currency, description, sessionID,
		))
	}
	return []byte(fmt.Sprintf(
		`{"event":%q,"payload":{"payment":{"entity":{"id":"pay_1","order_id":"order_1","amount":%s,"currency":%q,"status":"captured","error_description":%q,"notes":{"paymentId":"pay_1","sessionId":%q}}}}}`,
		event, amount, currency, description, sessionID,
	))
}

func TestNameIsRazorpay(t *testing.T) {
	provider := razorpay.NewProvider(testKeyID, testKeySecret, testWebhookSecret, "", nil)

	if got := provider.Name(); got != payment.RazorpayProviderName {
		t.Errorf("name = %q, want %q", got, payment.RazorpayProviderName)
	}
}

func TestCreatePaymentSendsOrderAndReturnsOrderDetails(t *testing.T) {
	var method, path, authorization, contentType string
	var received map[string]any

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		authorization = r.Header.Get("Authorization")
		contentType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read order request: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode order request %q: %v", body, err)
		}
		received = decoded
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"order_1","amount":5000,"currency":"INR","receipt":"pay_1"}`)
	}))
	defer stub.Close()

	provider := newProvider(stub)

	result, err := provider.CreatePayment(context.Background(), createRequest())
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if result.OrderID != "order_1" {
		t.Errorf("order id = %q, want order_1", result.OrderID)
	}
	if result.KeyID != testKeyID {
		t.Errorf("key id = %q, want the configured key id", result.KeyID)
	}
	if result.ClientSecret != "" {
		t.Errorf("client secret = %q, want none for razorpay", result.ClientSecret)
	}
	if method != http.MethodPost || path != "/v1/orders" {
		t.Errorf("request = %s %s, want POST /v1/orders", method, path)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte(testKeyID+":"+testKeySecret)); authorization != want {
		t.Errorf("Authorization = %q, want the basic key pair", authorization)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	// The amount is the payment's integer minor-unit amount in paise: 5000
	// exactly, with no float conversion on the way out.
	if got, ok := received["amount"].(float64); !ok || got != 5000 {
		t.Errorf("amount = %v in %v, want exactly 5000", received["amount"], received)
	}
	if got := received["currency"]; got != domain.CurrencyINR {
		t.Errorf("currency = %v, want %q", got, domain.CurrencyINR)
	}
	if got := received["receipt"]; got != "pay_1" {
		t.Errorf("receipt = %v, want the payment id", got)
	}
	notes, ok := received["notes"].(map[string]any)
	if !ok {
		t.Fatalf("notes = %v in %v, want an object", received["notes"], received)
	}
	if len(notes) != 1 || notes["sessionId"] != "ses_1" {
		t.Errorf("notes = %v, want only sessionId ses_1", notes)
	}
}

func TestCreatePaymentWrapsServerErrorAsProviderUnavailable(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"code":"SERVER_ERROR","description":"razorpay is down"}}`)
	}))
	defer stub.Close()

	provider := newProvider(stub)

	if _, err := provider.CreatePayment(context.Background(), createRequest()); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
}

func TestCreatePaymentWrapsTransportErrorAsProviderUnavailable(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	provider := razorpay.NewProvider(testKeyID, testKeySecret, testWebhookSecret, stub.URL, stub.Client())
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

func TestCreatePaymentRejectsAResponseWithoutAnOrderID(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"amount":5000,"currency":"INR"}`)
	}))
	defer stub.Close()

	provider := newProvider(stub)

	result, err := provider.CreatePayment(context.Background(), createRequest())
	if err == nil {
		t.Fatalf("order id = %q, want an error for a response without one", result.OrderID)
	}
	if result.OrderID != "" {
		t.Errorf("order id = %q, want none", result.OrderID)
	}
	if errors.Is(err, domain.ErrProviderUnavailable) {
		t.Errorf("error = %v, want a plain error rather than ErrProviderUnavailable", err)
	}
	if strings.Contains(err.Error(), testKeySecret) {
		t.Errorf("error = %v, want no key secret in the message", err)
	}
}

func TestParseWebhookMapsCapturedEvent(t *testing.T) {
	provider := razorpay.NewProvider(testKeyID, testKeySecret, testWebhookSecret, "", nil)
	body := paymentEntity("payment.captured", "5000", domain.CurrencyINR, "", "ses_1")

	event, err := provider.ParseWebhook(context.Background(), webhookRequest(body, "evt_1"))
	if err != nil {
		t.Fatalf("parse webhook: %v", err)
	}
	if event.EventID != "evt_1" {
		t.Errorf("event id = %q, want evt_1", event.EventID)
	}
	if event.PaymentID != "pay_1" {
		t.Errorf("payment id = %q, want pay_1", event.PaymentID)
	}
	if event.SessionID != "ses_1" {
		t.Errorf("session id = %q, want ses_1", event.SessionID)
	}
	if event.Status != domain.PaymentPaid {
		t.Errorf("status = %q, want %q", event.Status, domain.PaymentPaid)
	}
	if event.FailureMessage != "" {
		t.Errorf("failure message = %q, want none for a captured payment", event.FailureMessage)
	}
	if event.Amount == nil || *event.Amount != 5000 {
		t.Errorf("amount = %v, want 5000", event.Amount)
	}
	if event.Currency != domain.CurrencyINR {
		t.Errorf("currency = %q, want %q", event.Currency, domain.CurrencyINR)
	}
}

func TestParseWebhookMapsFailedEventWithFailureReason(t *testing.T) {
	provider := razorpay.NewProvider(testKeyID, testKeySecret, testWebhookSecret, "", nil)
	body := paymentEntity("payment.failed", "5000", domain.CurrencyINR, "Your card was declined.", "ses_1")

	event, err := provider.ParseWebhook(context.Background(), webhookRequest(body, "evt_2"))
	if err != nil {
		t.Fatalf("parse webhook: %v", err)
	}
	if event.EventID != "evt_2" {
		t.Errorf("event id = %q, want evt_2", event.EventID)
	}
	if event.Status != domain.PaymentFailed {
		t.Errorf("status = %q, want %q", event.Status, domain.PaymentFailed)
	}
	if event.FailureMessage != "Your card was declined." {
		t.Errorf("failure message = %q, want the razorpay reason", event.FailureMessage)
	}
}

func TestParseWebhookReportsNoAmountWhenTheEventCarriesNone(t *testing.T) {
	provider := razorpay.NewProvider(testKeyID, testKeySecret, testWebhookSecret, "", nil)
	body := paymentEntity("payment.captured", "", "", "", "ses_1")

	event, err := provider.ParseWebhook(context.Background(), webhookRequest(body, "evt_3"))
	if err != nil {
		t.Fatalf("parse webhook: %v", err)
	}
	if event.Amount != nil {
		t.Errorf("amount = %v, want nil for an event that reports none", *event.Amount)
	}
	if event.Currency != "" {
		t.Errorf("currency = %q, want it empty", event.Currency)
	}
}

func TestParseWebhookRejectsBadSignatures(t *testing.T) {
	provider := razorpay.NewProvider(testKeyID, testKeySecret, testWebhookSecret, "", nil)
	body := paymentEntity("payment.captured", "5000", domain.CurrencyINR, "", "ses_1")

	cases := []struct {
		name   string
		header string
	}{
		{name: "missing header", header: ""},
		{name: "wrong secret", header: sign("whsec_someone_else", body)},
		{name: "tampered body", header: sign(testWebhookSecret, []byte(`{"event":"payment.captured"}`))},
		{name: "not hex", header: "not-a-signature"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := provider.ParseWebhook(context.Background(), payment.WebhookRequest{
				Body:   body,
				Header: map[string]string{"x-razorpay-signature": tc.header},
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

func TestParseWebhookRejectsAnEmptyConfiguredSecret(t *testing.T) {
	provider := razorpay.NewProvider(testKeyID, testKeySecret, "", "", nil)
	body := paymentEntity("payment.captured", "5000", domain.CurrencyINR, "", "ses_1")

	_, err := provider.ParseWebhook(context.Background(), payment.WebhookRequest{
		Body:   body,
		Header: map[string]string{"x-razorpay-signature": sign("", body)},
	})

	var validation *domain.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestParseWebhookRejectsUnsupportedEventType(t *testing.T) {
	provider := razorpay.NewProvider(testKeyID, testKeySecret, testWebhookSecret, "", nil)
	body := paymentEntity("payment.authorized", "5000", domain.CurrencyINR, "", "ses_1")

	_, err := provider.ParseWebhook(context.Background(), webhookRequest(body, "evt_6"))

	var validation *domain.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestVerifySignatureAcceptsAValidHeader(t *testing.T) {
	payload := []byte(`{"event":"payment.captured"}`)

	if err := razorpay.VerifySignature(payload, sign(testWebhookSecret, payload), testWebhookSecret); err != nil {
		t.Errorf("verify signature: %v", err)
	}
	if err := razorpay.VerifySignature(payload, sign("whsec_other", payload), testWebhookSecret); err == nil {
		t.Error("expected a signature error for a different secret")
	}
}
