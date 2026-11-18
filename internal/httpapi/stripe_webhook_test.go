package httpapi_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"github.com/techverito/parking-service/internal/payments"
	"github.com/techverito/parking-service/internal/store/memory"
)

const (
	// stripeTestSecretKey and stripeTestWebhookSecret are obviously fake
	// credentials the Stripe test router is wired with.
	stripeTestSecretKey     = "sk_test_webhook_suite"
	stripeTestWebhookSecret = "whsec_test_webhook_suite"
	// stripeWebhookPath is the fixed URL Stripe is told to call.
	stripeWebhookPath = "/api/v1/payments/stripe/webhook"
)

// newStripeTestServer builds the real router with the Stripe provider pointed at
// the stub API and returns the payment store too, so a test can assert what a
// checkout persisted where no route exposes it.
func newStripeTestServer(t *testing.T, stub *httptest.Server, webhookSecret string) (*httptest.Server, *memory.PaymentStore) {
	t.Helper()

	provider, err := payments.FromSettings(payments.Settings{
		Name:                payment.StripeProviderName,
		StripeSecretKey:     stripeTestSecretKey,
		StripeWebhookSecret: webhookSecret,
		StripeBaseURL:       stub.URL,
	})
	if err != nil {
		t.Fatalf("build stripe payment provider: %v", err)
	}
	return newTestServerWithProvider(t, nil, provider)
}

// signStripePayload returns the Stripe-Signature header the provider verifies:
// t=<unix>,v1=<hex hmac over "<timestamp>.<payload>">.
func signStripePayload(t *testing.T, secret string, payload []byte, timestamp time.Time) string {
	t.Helper()

	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp.Unix())
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", timestamp.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

// stripeIntentRequest is one PaymentIntent creation the stub Stripe API received.
type stripeIntentRequest struct {
	method        string
	path          string
	authorization string
	form          url.Values
}

// stripeStub records what the service sent to the stub Stripe API.
type stripeStub struct {
	intents []stripeIntentRequest
}

// newStripeStub starts a stub Stripe API that records every PaymentIntent it is
// asked to create and answers with the given client secret.
func newStripeStub(t *testing.T, clientSecret string) (*httptest.Server, *stripeStub) {
	t.Helper()

	recorded := &stripeStub{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse stripe stub form: %v", err)
		}
		recorded.intents = append(recorded.intents, stripeIntentRequest{
			method:        r.Method,
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			form:          r.PostForm,
		})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"pi_stub","client_secret":%q}`, clientSecret)
	}))
	t.Cleanup(server.Close)
	return server, recorded
}

// newStripeFailureStub starts a stub Stripe API that answers every call with a
// 500, so a checkout sees the provider as unavailable.
func newStripeFailureStub(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"stripe is down"}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// stripeEventBody builds the event Stripe sends for a payment intent. A
// non-empty failure message becomes last_payment_error.message.
func stripeEventBody(eventID, eventType, paymentID, failureMessage string) []byte {
	if failureMessage == "" {
		return []byte(fmt.Sprintf(`{"id":%q,"type":%q,"data":{"object":{"metadata":{"paymentId":%q}}}}`, eventID, eventType, paymentID))
	}
	return []byte(fmt.Sprintf(`{"id":%q,"type":%q,"data":{"object":{"metadata":{"paymentId":%q},"last_payment_error":{"message":%q}}}}`, eventID, eventType, paymentID, failureMessage))
}

// postStripeEvent posts payload to the Stripe webhook route with the given
// Stripe-Signature header and returns the response and its raw body.
func postStripeEvent(t *testing.T, server *httptest.Server, payload []byte, signature string) (*http.Response, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, server.URL+stripeWebhookPath, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build stripe webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if signature != "" {
		req.Header.Set("Stripe-Signature", signature)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("post stripe webhook: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read stripe webhook response: %v", err)
	}
	return resp, body
}

// fetchPayment reads a payment back through the API and returns the decoded
// body with the raw JSON, so a test can assert both a value and its omission.
func fetchPayment(t *testing.T, server *httptest.Server, paymentID string) (paymentBody, map[string]json.RawMessage) {
	t.Helper()

	resp, payload := request(t, server, http.MethodGet, "/api/v1/payments/"+paymentID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get payment status = %d, body = %s", resp.StatusCode, payload)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode response %q: %v", payload, err)
	}
	return decode[paymentBody](t, payload), raw
}

// TestStripeWebhookCoversSuccessFailureSignatureAndDuplicates drives the Stripe
// provider end to end through the real router against a stub Stripe API: the
// checkout with its client secret, the signed success and failure callbacks, a
// callback that does not verify, replayed and late events, and the provider
// failure that answers 502 with the payment still PENDING.
func TestStripeWebhookCoversSuccessFailureSignatureAndDuplicates(t *testing.T) {
	t.Run("checkout creates a payment intent and returns its client secret", func(t *testing.T) {
		stub, recorded := newStripeStub(t, "cs_test_checkout")
		server, _ := newStripeTestServer(t, stub, stripeTestWebhookSecret)

		lot, session := checkoutAndPay(t, server, 1)
		created := session.Payment
		if created == nil {
			t.Fatalf("checkout response %+v has no payment", session)
		}
		if created.ClientSecret != "cs_test_checkout" {
			t.Errorf("clientSecret = %q, want the stub client secret", created.ClientSecret)
		}
		if created.Status != "PENDING" {
			t.Errorf("status = %q, want PENDING", created.Status)
		}
		if created.Currency != domain.CurrencyINR {
			t.Errorf("currency = %q, want %q", created.Currency, domain.CurrencyINR)
		}
		if created.Provider != payment.StripeProviderName {
			t.Errorf("provider = %q, want %q", created.Provider, payment.StripeProviderName)
		}
		if created.SessionID != session.ID || created.LotID != lot.ID {
			t.Errorf("payment = %+v, want the session %q and lot %q", created, session.ID, lot.ID)
		}

		if len(recorded.intents) != 1 {
			t.Fatalf("payment intents = %d, want exactly one", len(recorded.intents))
		}
		intent := recorded.intents[0]
		if intent.method != http.MethodPost || intent.path != "/v1/payment_intents" {
			t.Errorf("request = %s %s, want POST /v1/payment_intents", intent.method, intent.path)
		}
		if intent.authorization != "Bearer "+stripeTestSecretKey {
			t.Errorf("Authorization = %q, want the bearer secret key", intent.authorization)
		}
		if want := fmt.Sprintf("%d", testHourlyRate); intent.form.Get("amount") != want {
			t.Errorf("amount = %q, want %q", intent.form.Get("amount"), want)
		}
		if got := intent.form.Get("currency"); got != domain.CurrencyINR {
			t.Errorf("currency = %q, want %q", got, domain.CurrencyINR)
		}
		if got := intent.form.Get("metadata[sessionId]"); got != session.ID {
			t.Errorf("metadata[sessionId] = %q, want %q", got, session.ID)
		}
		if got := intent.form.Get("metadata[paymentId]"); got != created.ID {
			t.Errorf("metadata[paymentId] = %q, want %q", got, created.ID)
		}

		// The client secret stays readable while the payment is pending, so a
		// client that lost the checkout response can still confirm the intent.
		stored, raw := fetchPayment(t, server, created.ID)
		if stored.ClientSecret != "cs_test_checkout" {
			t.Errorf("get clientSecret = %q, want the client secret while the payment is pending", stored.ClientSecret)
		}
		if _, ok := raw["clientSecret"]; !ok {
			t.Errorf("get payment body %v has no clientSecret, want it while the payment is pending", raw)
		}
	})

	t.Run("a signed success event marks the payment paid", func(t *testing.T) {
		stub, _ := newStripeStub(t, "cs_test_paid")
		server, _ := newStripeTestServer(t, stub, stripeTestWebhookSecret)
		_, session := checkoutAndPay(t, server, 1)
		if session.Payment == nil {
			t.Fatalf("checkout response %+v has no payment", session)
		}

		body := stripeEventBody("evt_paid", "payment_intent.succeeded", session.Payment.ID, "")
		resp, payload := postStripeEvent(t, server, body, signStripePayload(t, stripeTestWebhookSecret, body, time.Now()))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("webhook status = %d, body = %s", resp.StatusCode, payload)
		}
		if settled := decode[paymentBody](t, payload); settled.Status != "PAID" {
			t.Errorf("status = %q, want PAID", settled.Status)
		}

		stored, raw := fetchPayment(t, server, session.Payment.ID)
		if stored.Status != "PAID" {
			t.Errorf("get status = %q, want PAID", stored.Status)
		}
		if _, ok := raw["clientSecret"]; ok {
			t.Errorf("get payment body %v still carries clientSecret, want it cleared once paid", raw)
		}
		if _, ok := raw["failureMessage"]; ok {
			t.Errorf("get payment body %v carries failureMessage, want it omitted for a paid payment", raw)
		}
	})

	t.Run("a signed failure event stores the failure message", func(t *testing.T) {
		stub, _ := newStripeStub(t, "cs_test_failed")
		server, _ := newStripeTestServer(t, stub, stripeTestWebhookSecret)
		_, session := checkoutAndPay(t, server, 1)
		if session.Payment == nil {
			t.Fatalf("checkout response %+v has no payment", session)
		}

		body := stripeEventBody("evt_failed", "payment_intent.payment_failed", session.Payment.ID, "Your card was declined.")
		resp, payload := postStripeEvent(t, server, body, signStripePayload(t, stripeTestWebhookSecret, body, time.Now()))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("webhook status = %d, body = %s", resp.StatusCode, payload)
		}
		settled := decode[paymentBody](t, payload)
		if settled.Status != "FAILED" {
			t.Errorf("status = %q, want FAILED", settled.Status)
		}
		if settled.FailureMessage != "Your card was declined." {
			t.Errorf("failureMessage = %q, want the stripe message", settled.FailureMessage)
		}

		stored, raw := fetchPayment(t, server, session.Payment.ID)
		if stored.Status != "FAILED" || stored.FailureMessage != "Your card was declined." {
			t.Errorf("payment = %+v, want FAILED with the failure message", stored)
		}
		message, ok := raw["failureMessage"]
		if !ok || string(message) != `"Your card was declined."` {
			t.Errorf("failureMessage = %s in %v, want the stripe message", message, raw)
		}
		if _, ok := raw["clientSecret"]; ok {
			t.Errorf("get payment body %v still carries clientSecret, want it cleared once failed", raw)
		}
	})

	t.Run("a callback that does not verify changes nothing", func(t *testing.T) {
		stub, _ := newStripeStub(t, "cs_test_signature")
		server, _ := newStripeTestServer(t, stub, stripeTestWebhookSecret)
		_, session := checkoutAndPay(t, server, 1)
		if session.Payment == nil {
			t.Fatalf("checkout response %+v has no payment", session)
		}
		before, _ := fetchPayment(t, server, session.Payment.ID)

		succeeded := stripeEventBody("evt_tampered", "payment_intent.succeeded", session.Payment.ID, "")
		tampered := stripeEventBody("evt_tampered", "payment_intent.payment_failed", session.Payment.ID, "Your card was declined.")

		cases := []struct {
			name      string
			payload   []byte
			signature string
		}{
			{
				name:      "tampered body",
				payload:   tampered,
				signature: signStripePayload(t, stripeTestWebhookSecret, succeeded, time.Now()),
			},
			{
				name:      "wrong secret",
				payload:   succeeded,
				signature: signStripePayload(t, "whsec_someone_else", succeeded, time.Now()),
			},
			{
				name:      "missing header",
				payload:   succeeded,
				signature: "",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				resp, payload := postStripeEvent(t, server, tc.payload, tc.signature)
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400, body = %s", resp.StatusCode, payload)
				}
				if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
					t.Errorf("Content-Type = %q, want application/problem+json", contentType)
				}
				problem := decode[problemBody](t, payload)
				if problem.Code != "VALIDATION_FAILED" {
					t.Errorf("code = %q, want VALIDATION_FAILED", problem.Code)
				}
				if problem.Type == "" || problem.Title == "" || problem.Detail == "" || problem.Status != http.StatusBadRequest {
					t.Errorf("problem = %+v, want the full problem+json envelope", problem)
				}
				if problem.Instance != stripeWebhookPath {
					t.Errorf("instance = %q, want %q", problem.Instance, stripeWebhookPath)
				}

				after, _ := fetchPayment(t, server, session.Payment.ID)
				if after.Status != before.Status || !after.UpdatedAt.Equal(before.UpdatedAt) {
					t.Errorf("payment = %+v, want the unchanged %+v", after, before)
				}
			})
		}
	})

	t.Run("replayed and late events answer 200 and change nothing", func(t *testing.T) {
		stub, _ := newStripeStub(t, "cs_test_repeat")
		server, _ := newStripeTestServer(t, stub, stripeTestWebhookSecret)
		_, session := checkoutAndPay(t, server, 1)
		if session.Payment == nil {
			t.Fatalf("checkout response %+v has no payment", session)
		}

		paid := stripeEventBody("evt_repeat", "payment_intent.succeeded", session.Payment.ID, "")
		resp, payload := postStripeEvent(t, server, paid, signStripePayload(t, stripeTestWebhookSecret, paid, time.Now()))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("first webhook status = %d, body = %s", resp.StatusCode, payload)
		}
		settled, _ := fetchPayment(t, server, session.Payment.ID)
		if settled.Status != "PAID" {
			t.Fatalf("status = %q, want PAID", settled.Status)
		}

		// The same event id again is the callback Stripe retries after a
		// timeout: it answers 200 and the payment is untouched.
		resp, payload = postStripeEvent(t, server, paid, signStripePayload(t, stripeTestWebhookSecret, paid, time.Now()))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("replayed webhook status = %d, body = %s", resp.StatusCode, payload)
		}
		repeated, _ := fetchPayment(t, server, session.Payment.ID)
		if repeated.Status != settled.Status || repeated.FailureMessage != "" || !repeated.UpdatedAt.Equal(settled.UpdatedAt) {
			t.Errorf("payment = %+v, want the unchanged %+v", repeated, settled)
		}

		// A different event for the now-terminal payment changes nothing either.
		failed := stripeEventBody("evt_late", "payment_intent.payment_failed", session.Payment.ID, "Your card was declined.")
		resp, payload = postStripeEvent(t, server, failed, signStripePayload(t, stripeTestWebhookSecret, failed, time.Now()))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("late webhook status = %d, body = %s", resp.StatusCode, payload)
		}
		terminal, _ := fetchPayment(t, server, session.Payment.ID)
		if terminal.Status != settled.Status || terminal.FailureMessage != "" || !terminal.UpdatedAt.Equal(settled.UpdatedAt) {
			t.Errorf("payment = %+v, want the unchanged %+v", terminal, settled)
		}
	})

	t.Run("the fake provider webhook still answers 409 for a terminal payment", func(t *testing.T) {
		server := newTestServer(t)
		_, session := checkoutAndPay(t, server, 1)
		if session.Payment == nil {
			t.Fatalf("checkout response %+v has no payment", session)
		}

		resp, payload := request(t, server, http.MethodPost, "/api/v1/payments/webhooks/"+payment.FakeProviderName, map[string]any{
			"paymentId": session.Payment.ID,
			"status":    "PAID",
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("first webhook status = %d, body = %s", resp.StatusCode, payload)
		}

		resp, payload = request(t, server, http.MethodPost, "/api/v1/payments/webhooks/"+payment.FakeProviderName, map[string]any{
			"paymentId": session.Payment.ID,
			"status":    "PAID",
		})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("replayed webhook status = %d, want 409, body = %s", resp.StatusCode, payload)
		}
		if got := decode[problemBody](t, payload).Code; got != "PAYMENT_NOT_PENDING" {
			t.Errorf("code = %q, want PAYMENT_NOT_PENDING", got)
		}
	})

	t.Run("a stripe 5xx answers 502 and leaves the payment pending", func(t *testing.T) {
		stub := newStripeFailureStub(t)
		server, paymentStore := newStripeTestServer(t, stub, stripeTestWebhookSecret)

		lot := createLot(t, server, 1)
		session := checkInVehicle(t, server, lot.ID, "KA 01 AB 1234")

		resp, payload := request(t, server, http.MethodPost, "/api/v1/sessions/"+session.ID+"/checkout", nil)
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("checkout status = %d, want 502, body = %s", resp.StatusCode, payload)
		}
		problem := decode[problemBody](t, payload)
		if problem.Code != "PROVIDER_ERROR" {
			t.Errorf("code = %q, want PROVIDER_ERROR", problem.Code)
		}
		if problem.Status != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", problem.Status)
		}
		if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
			t.Errorf("Content-Type = %q, want application/problem+json", contentType)
		}

		// The session was written before the payment was opened, so the vehicle
		// stays checked out and the attempt left a PENDING payment behind.
		resp, payload = request(t, server, http.MethodGet, "/api/v1/sessions/"+session.ID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get session status = %d, body = %s", resp.StatusCode, payload)
		}
		checkedOut := decode[sessionBody](t, payload)
		if checkedOut.Status != "CHECKED_OUT" || checkedOut.CheckedOutAt == nil {
			t.Errorf("session = %+v, want a checked-out session with a timestamp", checkedOut)
		}

		pending, err := paymentStore.GetBySession(context.Background(), session.ID)
		if err != nil {
			t.Fatalf("get payment by session: %v", err)
		}
		if pending.Status != domain.PaymentPending {
			t.Errorf("payment status = %q, want %q", pending.Status, domain.PaymentPending)
		}
		if pending.ClientSecret != "" {
			t.Errorf("client secret = %q, want none when the provider failed", pending.ClientSecret)
		}
	})
}
