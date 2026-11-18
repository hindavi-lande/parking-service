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
	"strings"
	"testing"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/payment"
	"github.com/techverito/parking-service/internal/payments"
	"github.com/techverito/parking-service/internal/store/memory"
)

const (
	// razorpayTestKeyID, razorpayTestKeySecret and razorpayTestWebhookSecret
	// are obviously fake credentials the Razorpay test router is wired with.
	razorpayTestKeyID         = "rzp_test_webhook_suite"
	razorpayTestKeySecret     = "rzp_secret_webhook_suite"
	razorpayTestWebhookSecret = "whsec_razorpay_webhook_suite"
	// razorpayWebhookPath is the fixed URL Razorpay is told to call.
	razorpayWebhookPath = "/api/v1/payments/razorpay/webhook"
)

// newRazorpayTestServer builds the real router with the Razorpay provider
// pointed at the stub API and returns the payment store too, so a test can
// assert what a checkout persisted where no route exposes it.
func newRazorpayTestServer(t *testing.T, stub *httptest.Server, webhookSecret string) (*httptest.Server, *memory.PaymentStore) {
	t.Helper()

	provider, err := payments.FromSettings(payments.Settings{
		Name:                  payment.RazorpayProviderName,
		RazorpayKeyID:         razorpayTestKeyID,
		RazorpayKeySecret:     razorpayTestKeySecret,
		RazorpayWebhookSecret: webhookSecret,
		RazorpayBaseURL:       stub.URL,
	})
	if err != nil {
		t.Fatalf("build razorpay payment provider: %v", err)
	}
	return newTestServerWithProvider(t, nil, provider)
}

// razorpayOrderBody is the order creation body the stub Razorpay API receives.
// Amount is the payment's integer minor-unit amount in paise.
type razorpayOrderBody struct {
	Amount   int64             `json:"amount"`
	Currency string            `json:"currency"`
	Receipt  string            `json:"receipt"`
	Notes    map[string]string `json:"notes"`
}

// razorpayOrderRequest is one order creation the stub Razorpay API received.
type razorpayOrderRequest struct {
	method        string
	path          string
	authorization string
	body          razorpayOrderBody
}

// razorpayStub records what the service sent to the stub Razorpay API.
type razorpayStub struct {
	orders []razorpayOrderRequest
}

// newRazorpayStub starts a stub Razorpay API that records every order it is
// asked to create and answers with the given order id.
func newRazorpayStub(t *testing.T, orderID string) (*httptest.Server, *razorpayStub) {
	t.Helper()

	recorded := &razorpayStub{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read razorpay stub order request: %v", err)
		}
		var decoded razorpayOrderBody
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode razorpay stub order request %q: %v", body, err)
		}
		recorded.orders = append(recorded.orders, razorpayOrderRequest{
			method:        r.Method,
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			body:          decoded,
		})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"amount":%d,"currency":"INR","receipt":%q}`, orderID, decoded.Amount, decoded.Receipt)
	}))
	t.Cleanup(server.Close)
	return server, recorded
}

// newRazorpayFailureStub starts a stub Razorpay API that answers every call with
// a 500, so a checkout sees the provider as unavailable.
func newRazorpayFailureStub(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"code":"SERVER_ERROR","description":"razorpay is down"}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// signRazorpayPayload returns the X-Razorpay-Signature header the provider
// verifies: the hex HMAC-SHA256 of the exact raw body with the webhook secret.
func signRazorpayPayload(t *testing.T, secret string, payload []byte) string {
	t.Helper()

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// razorpayEventBody builds the event Razorpay sends for a payment. An empty
// amount or currency is left out of the entity, and a non-empty failure message
// becomes error_description.
func razorpayEventBody(eventType, orderID, amount, currency, errorDescription, sessionID string) []byte {
	entity := fmt.Sprintf(`"id":"pay_razorpay_1","order_id":%q,"status":"captured","notes":{"sessionId":%q}`, orderID, sessionID)
	if amount != "" {
		entity += `,"amount":` + amount
	}
	if currency != "" {
		entity += fmt.Sprintf(`,"currency":%q`, currency)
	}
	if errorDescription != "" {
		entity += fmt.Sprintf(`,"error_description":%q`, errorDescription)
	}
	return []byte(fmt.Sprintf(`{"event":%q,"payload":{"payment":{"entity":{%s}}}}`, eventType, entity))
}

// postRazorpayEvent posts payload to the Razorpay webhook route with the given
// X-Razorpay-Signature and X-Razorpay-Event-Id headers and returns the response
// and its raw body.
func postRazorpayEvent(t *testing.T, server *httptest.Server, payload []byte, signature, eventID string) (*http.Response, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, server.URL+razorpayWebhookPath, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build razorpay webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if signature != "" {
		req.Header.Set("X-Razorpay-Signature", signature)
	}
	if eventID != "" {
		req.Header.Set("X-Razorpay-Event-Id", eventID)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("post razorpay webhook: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read razorpay webhook response: %v", err)
	}
	return resp, body
}

func TestRazorpayWebhookCapturedMarksPaid(t *testing.T) {
	stub, _ := newRazorpayStub(t, "order_captured")
	server, _ := newRazorpayTestServer(t, stub, razorpayTestWebhookSecret)

	_, session := checkoutAndPay(t, server, 1)
	if session.Payment == nil {
		t.Fatalf("checkout response %+v has no payment", session)
	}

	body := razorpayEventBody("payment.captured", "order_captured", fmt.Sprintf("%d", session.Payment.Amount), domain.CurrencyINR, "", session.ID)
	resp, payload := postRazorpayEvent(t, server, body, signRazorpayPayload(t, razorpayTestWebhookSecret, body), "evt_razorpay_paid")
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
	// The order details survive the status change, so the payment still reads
	// back the order the client paid against.
	if stored.OrderID != "order_captured" || stored.KeyID != razorpayTestKeyID {
		t.Errorf("payment = %+v, want the order details kept", stored)
	}
	if _, ok := raw["clientSecret"]; ok {
		t.Errorf("get payment body %v carries clientSecret, want none for razorpay", raw)
	}
}

func TestRazorpayWebhookFailedMarksFailedWithReason(t *testing.T) {
	stub, _ := newRazorpayStub(t, "order_failed")
	server, _ := newRazorpayTestServer(t, stub, razorpayTestWebhookSecret)

	_, session := checkoutAndPay(t, server, 1)
	if session.Payment == nil {
		t.Fatalf("checkout response %+v has no payment", session)
	}

	body := razorpayEventBody("payment.failed", "order_failed", fmt.Sprintf("%d", session.Payment.Amount), domain.CurrencyINR, "Your card was declined.", session.ID)
	resp, payload := postRazorpayEvent(t, server, body, signRazorpayPayload(t, razorpayTestWebhookSecret, body), "evt_razorpay_failed")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook status = %d, body = %s", resp.StatusCode, payload)
	}
	settled := decode[paymentBody](t, payload)
	if settled.Status != "FAILED" {
		t.Errorf("status = %q, want FAILED", settled.Status)
	}
	if settled.FailureMessage != "Your card was declined." {
		t.Errorf("failureMessage = %q, want the razorpay reason", settled.FailureMessage)
	}

	stored, raw := fetchPayment(t, server, session.Payment.ID)
	if stored.Status != "FAILED" || stored.FailureMessage != "Your card was declined." {
		t.Errorf("payment = %+v, want FAILED with the failure message", stored)
	}
	message, ok := raw["failureMessage"]
	if !ok || string(message) != `"Your card was declined."` {
		t.Errorf("failureMessage = %s in %v, want the razorpay reason", message, raw)
	}
}

func TestRazorpayWebhookRejectsBadSignature(t *testing.T) {
	stub, _ := newRazorpayStub(t, "order_signature")
	server, _ := newRazorpayTestServer(t, stub, razorpayTestWebhookSecret)

	_, session := checkoutAndPay(t, server, 1)
	if session.Payment == nil {
		t.Fatalf("checkout response %+v has no payment", session)
	}
	before, _ := fetchPayment(t, server, session.Payment.ID)

	body := razorpayEventBody("payment.captured", "order_signature", fmt.Sprintf("%d", session.Payment.Amount), domain.CurrencyINR, "", session.ID)

	cases := []struct {
		name      string
		payload   []byte
		signature string
	}{
		{name: "wrong secret", payload: body, signature: signRazorpayPayload(t, "whsec_someone_else", body)},
		{name: "missing header", payload: body, signature: ""},
		{name: "tampered body", payload: body, signature: signRazorpayPayload(t, razorpayTestWebhookSecret, []byte(`{"event":"payment.captured"}`))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, payload := postRazorpayEvent(t, server, tc.payload, tc.signature, "evt_razorpay_signature")
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
			if problem.Instance != razorpayWebhookPath {
				t.Errorf("instance = %q, want %q", problem.Instance, razorpayWebhookPath)
			}

			after, _ := fetchPayment(t, server, session.Payment.ID)
			if after.Status != before.Status || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Errorf("payment = %+v, want the unchanged %+v", after, before)
			}
		})
	}
}

// TestRazorpayWebhookRejectsAmountMismatchAndConsumesTheEvent covers a captured
// event that reports an amount the payment was not created for: the route
// answers 400 and the payment stays PENDING, while the event id is consumed so a
// replay is answered 200 without changes.
func TestRazorpayWebhookRejectsAmountMismatchAndConsumesTheEvent(t *testing.T) {
	stub, _ := newRazorpayStub(t, "order_mismatch")
	server, _ := newRazorpayTestServer(t, stub, razorpayTestWebhookSecret)

	_, session := checkoutAndPay(t, server, 1)
	if session.Payment == nil {
		t.Fatalf("checkout response %+v has no payment", session)
	}

	body := razorpayEventBody("payment.captured", "order_mismatch", fmt.Sprintf("%d", session.Payment.Amount+1), domain.CurrencyINR, "", session.ID)
	resp, payload := postRazorpayEvent(t, server, body, signRazorpayPayload(t, razorpayTestWebhookSecret, body), "evt_razorpay_mismatch")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", resp.StatusCode, payload)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", contentType)
	}
	if got := decode[problemBody](t, payload).Code; got != "VALIDATION_FAILED" {
		t.Errorf("code = %q, want VALIDATION_FAILED", got)
	}

	rejected, _ := fetchPayment(t, server, session.Payment.ID)
	if rejected.Status != "PENDING" {
		t.Errorf("status = %q, want PENDING after a rejected event", rejected.Status)
	}

	// The event was consumed, so re-posting the same event id answers 200 and
	// changes nothing.
	resp, payload = postRazorpayEvent(t, server, body, signRazorpayPayload(t, razorpayTestWebhookSecret, body), "evt_razorpay_mismatch")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replayed status = %d, want 200, body = %s", resp.StatusCode, payload)
	}
	if replayed := decode[paymentBody](t, payload); replayed.Status != "PENDING" {
		t.Errorf("status = %q, want PENDING", replayed.Status)
	}

	after, _ := fetchPayment(t, server, session.Payment.ID)
	if after.Status != rejected.Status || !after.UpdatedAt.Equal(rejected.UpdatedAt) {
		t.Errorf("payment = %+v, want the unchanged %+v", after, rejected)
	}
}

func TestRazorpayWebhookDuplicateEventIsIdempotent(t *testing.T) {
	stub, _ := newRazorpayStub(t, "order_duplicate")
	server, _ := newRazorpayTestServer(t, stub, razorpayTestWebhookSecret)

	_, session := checkoutAndPay(t, server, 1)
	if session.Payment == nil {
		t.Fatalf("checkout response %+v has no payment", session)
	}

	body := razorpayEventBody("payment.captured", "order_duplicate", fmt.Sprintf("%d", session.Payment.Amount), domain.CurrencyINR, "", session.ID)
	resp, payload := postRazorpayEvent(t, server, body, signRazorpayPayload(t, razorpayTestWebhookSecret, body), "evt_razorpay_duplicate")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook status = %d, body = %s", resp.StatusCode, payload)
	}
	settled, _ := fetchPayment(t, server, session.Payment.ID)
	if settled.Status != "PAID" {
		t.Fatalf("status = %q, want PAID", settled.Status)
	}

	// Razorpay retries a callback it could not deliver: the same event id again
	// answers 200 and the payment is untouched.
	resp, payload = postRazorpayEvent(t, server, body, signRazorpayPayload(t, razorpayTestWebhookSecret, body), "evt_razorpay_duplicate")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replayed status = %d, want 200, body = %s", resp.StatusCode, payload)
	}
	repeated, _ := fetchPayment(t, server, session.Payment.ID)
	if repeated.Status != "PAID" || !repeated.UpdatedAt.Equal(settled.UpdatedAt) {
		t.Errorf("payment = %+v, want the unchanged %+v", repeated, settled)
	}
}

// TestCheckoutWithRazorpayReturnsOrderDetails asserts what a client needs to
// launch Razorpay Checkout: the order the service opened and the publishable key
// id, next to the amount in paise.
func TestCheckoutWithRazorpayReturnsOrderDetails(t *testing.T) {
	stub, recorded := newRazorpayStub(t, "order_checkout_1")
	server, _ := newRazorpayTestServer(t, stub, razorpayTestWebhookSecret)

	lot, session := checkoutAndPay(t, server, 1)
	created := session.Payment
	if created == nil {
		t.Fatalf("checkout response %+v has no payment", session)
	}
	if created.Status != "PENDING" {
		t.Errorf("status = %q, want PENDING", created.Status)
	}
	if created.Amount != testHourlyRate {
		t.Errorf("amount = %d, want the one-hour minimum %d in paise", created.Amount, testHourlyRate)
	}
	if created.Currency != domain.CurrencyINR {
		t.Errorf("currency = %q, want %q", created.Currency, domain.CurrencyINR)
	}
	if created.Provider != payment.RazorpayProviderName {
		t.Errorf("provider = %q, want %q", created.Provider, payment.RazorpayProviderName)
	}
	if created.SessionID != session.ID || created.LotID != lot.ID {
		t.Errorf("payment = %+v, want the session %q and lot %q", created, session.ID, lot.ID)
	}
	if created.OrderID != "order_checkout_1" {
		t.Errorf("orderId = %q, want the order id the stub returned", created.OrderID)
	}
	if created.KeyID != razorpayTestKeyID {
		t.Errorf("keyId = %q, want the configured key id", created.KeyID)
	}

	if len(recorded.orders) != 1 {
		t.Fatalf("orders = %d, want exactly one", len(recorded.orders))
	}
	order := recorded.orders[0]
	if order.method != http.MethodPost || order.path != "/v1/orders" {
		t.Errorf("request = %s %s, want POST /v1/orders", order.method, order.path)
	}
	if !strings.HasPrefix(order.authorization, "Basic ") {
		t.Errorf("Authorization = %q, want basic auth", order.authorization)
	}
	if order.body.Amount != int64(testHourlyRate) {
		t.Errorf("order amount = %d, want %d", order.body.Amount, testHourlyRate)
	}
	if order.body.Currency != domain.CurrencyINR {
		t.Errorf("order currency = %q, want %q", order.body.Currency, domain.CurrencyINR)
	}
	if order.body.Receipt != created.ID {
		t.Errorf("order receipt = %q, want the payment id %q", order.body.Receipt, created.ID)
	}
	if len(order.body.Notes) != 1 || order.body.Notes["sessionId"] != session.ID {
		t.Errorf("order notes = %v, want only sessionId %q", order.body.Notes, session.ID)
	}

	// The order details have a read path of their own: the stored payment keeps
	// them, so a client that lost the checkout response still finds them.
	stored, _ := fetchPayment(t, server, created.ID)
	if stored.OrderID != "order_checkout_1" || stored.KeyID != razorpayTestKeyID {
		t.Errorf("get payment = %+v, want the stored order details", stored)
	}
}

// TestCheckoutRetryAfterRazorpayFailureReturnsSamePayment covers a provider
// timeout or 5xx during checkout: the first call answers 502 PROVIDER_ERROR and
// leaves the payment PENDING, and repeating the checkout answers 200 with that
// same payment. The stub fails every order request, so a retry that reached the
// provider again would answer 502 rather than 200.
func TestCheckoutRetryAfterRazorpayFailureReturnsSamePayment(t *testing.T) {
	stub := newRazorpayFailureStub(t)
	server, paymentStore := newRazorpayTestServer(t, stub, razorpayTestWebhookSecret)

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
	if pending.ProviderOrderID != "" || pending.ProviderKeyID != "" {
		t.Errorf("payment = %+v, want no order details when the provider failed", pending)
	}

	resp, payload = request(t, server, http.MethodPost, "/api/v1/sessions/"+session.ID+"/checkout", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retried checkout status = %d, want 200, body = %s", resp.StatusCode, payload)
	}
	retried := decode[sessionBody](t, payload)
	if retried.Payment == nil {
		t.Fatalf("retried checkout response %+v has no payment", retried)
	}
	if retried.Payment.ID != pending.ID {
		t.Errorf("payment id = %q, want the only payment %q", retried.Payment.ID, pending.ID)
	}
	if retried.Payment.Status != "PENDING" {
		t.Errorf("status = %q, want PENDING", retried.Payment.Status)
	}
}
