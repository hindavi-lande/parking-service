package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/httpapi"
	"github.com/techverito/parking-service/internal/notify"
	"github.com/techverito/parking-service/internal/payment"
	"github.com/techverito/parking-service/internal/payments"
	"github.com/techverito/parking-service/internal/service"
	"github.com/techverito/parking-service/internal/store/memory"
)

// testHourlyRate is the PAYMENT_HOURLY_RATE the test router is wired with, in
// minor units. Check-ins and checkouts happen within the same second in these
// tests, so a stay is billed the one-hour minimum.
const testHourlyRate = 5000

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	return newTestServerWithNotifier(t, nil)
}

// newTestServerWithNotifier builds the real router with the given notifier, so a
// test can assert what the service tried to send.
func newTestServerWithNotifier(t *testing.T, notifier notify.Notifier) *httptest.Server {
	t.Helper()

	server, _ := newTestServerWithStores(t, notifier)
	return server
}

// newTestServerWithStores builds the real router with the fake payment provider
// and returns the payment store too, so a test can assert which payments a
// session received.
func newTestServerWithStores(t *testing.T, notifier notify.Notifier) (*httptest.Server, *memory.PaymentStore) {
	t.Helper()

	provider, err := payments.FromSettings(payments.Settings{Name: payment.FakeProviderName})
	if err != nil {
		t.Fatalf("build payment provider: %v", err)
	}
	return newTestServerWithProvider(t, notifier, provider)
}

// newTestServerWithProvider builds the real router around the given payment
// provider and returns the payment store too, so a test can assert what the
// checkout persisted after a provider failure.
func newTestServerWithProvider(t *testing.T, notifier notify.Notifier, provider payment.Provider) (*httptest.Server, *memory.PaymentStore) {
	t.Helper()

	lots := memory.NewLotStore()
	sessions := memory.NewSessionStore()
	paymentStore := memory.NewPaymentStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	paymentService := service.NewPaymentService(paymentStore, lots, provider, testHourlyRate, time.Now)

	server := httptest.NewServer(httpapi.NewRouter(
		service.NewLotService(lots, time.Now),
		service.NewSessionService(lots, sessions, paymentService, time.Now, notifier, logger),
		paymentService,
		logger,
	))
	t.Cleanup(server.Close)
	return server, paymentStore
}

// recordingNotifier records the messages the service tried to send so the HTTP
// tests can assert on them without a provider.
type recordingNotifier struct {
	messages []notify.Message
	err      error
}

func (n *recordingNotifier) Send(_ context.Context, msg notify.Message) error {
	n.messages = append(n.messages, msg)
	return n.err
}

// unavailableProvider is a payment.Provider whose CreatePayment always reports
// the provider as unreachable, so the 502 mapping can be asserted through the
// real router without a network call.
type unavailableProvider struct{}

func (unavailableProvider) Name() string { return payment.FakeProviderName }

func (unavailableProvider) CreatePayment(_ context.Context, _ payment.CreateRequest) (payment.CreateResult, error) {
	return payment.CreateResult{}, fmt.Errorf("provider is down: %w", domain.ErrProviderUnavailable)
}

func (unavailableProvider) ParseWebhook(_ context.Context, _ payment.WebhookRequest) (payment.WebhookEvent, error) {
	return payment.WebhookEvent{}, errors.New("parse webhook is not used by this stub")
}

func request(t *testing.T, server *httptest.Server, method, path string, body any) (*http.Response, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	payload, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp, payload
}

func decode[T any](t *testing.T, payload []byte) T {
	t.Helper()

	var value T
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("decode response %q: %v", payload, err)
	}
	return value
}

type lotBody struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Address    string    `json:"address"`
	Capacity   int       `json:"capacity"`
	HourlyRate *int      `json:"hourlyRate"`
	CreatedAt  time.Time `json:"createdAt"`
}

type sessionBody struct {
	ID           string       `json:"id"`
	LotID        string       `json:"lotId"`
	Plate        string       `json:"plate"`
	Kind         string       `json:"kind"`
	Status       string       `json:"status"`
	CheckedInAt  time.Time    `json:"checkedInAt"`
	CheckedOutAt *time.Time   `json:"checkedOutAt"`
	Payment      *paymentBody `json:"payment"`
}

type paymentBody struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"sessionId"`
	LotID          string    `json:"lotId"`
	Amount         int64     `json:"amount"`
	Currency       string    `json:"currency"`
	Provider       string    `json:"provider"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	ClientSecret   string    `json:"clientSecret"`
	FailureMessage string    `json:"failureMessage"`
}

type problemBody struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
	Code     string `json:"code"`
}

func createLot(t *testing.T, server *httptest.Server, capacity int) lotBody {
	t.Helper()

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots", map[string]any{
		"name":     "Central Plaza",
		"address":  "MG Road",
		"capacity": capacity,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create lot status = %d, body = %s", resp.StatusCode, payload)
	}
	return decode[lotBody](t, payload)
}

// checkInVehicle parks one vehicle in the lot and returns the created session.
func checkInVehicle(t *testing.T, server *httptest.Server, lotID, plate string) sessionBody {
	t.Helper()

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots/"+lotID+"/check-ins", map[string]any{
		"plate": plate,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("check-in status = %d, body = %s", resp.StatusCode, payload)
	}
	return decode[sessionBody](t, payload)
}

// checkoutAndPay creates a lot, parks one vehicle and checks it out, returning
// the lot and the checkout response with its payment.
func checkoutAndPay(t *testing.T, server *httptest.Server, capacity int) (lotBody, sessionBody) {
	t.Helper()

	lot := createLot(t, server, capacity)
	session := checkInVehicle(t, server, lot.ID, "KA 01 AB 1234")

	resp, payload := request(t, server, http.MethodPost, "/api/v1/sessions/"+session.ID+"/checkout", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("checkout status = %d, body = %s", resp.StatusCode, payload)
	}
	return lot, decode[sessionBody](t, payload)
}

func TestHealth(t *testing.T) {
	server := newTestServer(t)

	resp, payload := request(t, server, http.MethodGet, "/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := decode[map[string]string](t, payload)["status"]; got != "ok" {
		t.Errorf("status = %q, want ok", got)
	}
}

func TestCreateListAndGetLot(t *testing.T) {
	server := newTestServer(t)
	lot := createLot(t, server, 2)

	if lot.ID == "" {
		t.Fatal("created lot has no id")
	}
	if lot.Name != "Central Plaza" || lot.Capacity != 2 {
		t.Errorf("lot = %+v, want the submitted values", lot)
	}
	if lot.CreatedAt.IsZero() {
		t.Error("createdAt is zero")
	}

	resp, payload := request(t, server, http.MethodGet, "/api/v1/lots", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", resp.StatusCode)
	}
	lots := decode[[]lotBody](t, payload)
	if len(lots) != 1 || lots[0].ID != lot.ID {
		t.Errorf("lots = %+v, want the created lot", lots)
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/lots/"+lot.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", resp.StatusCode)
	}
	if got := decode[lotBody](t, payload); got.ID != lot.ID {
		t.Errorf("lot id = %q, want %q", got.ID, lot.ID)
	}
}

func TestCreateLotWithHourlyRate(t *testing.T) {
	server := newTestServer(t)

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots", map[string]any{
		"name":       "Central Plaza",
		"address":    "MG Road",
		"capacity":   2,
		"hourlyRate": 5000,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create lot status = %d, body = %s", resp.StatusCode, payload)
	}
	lot := decode[lotBody](t, payload)
	if lot.HourlyRate == nil || *lot.HourlyRate != 5000 {
		t.Fatalf("hourlyRate = %v, want 5000", lot.HourlyRate)
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/lots/"+lot.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", resp.StatusCode)
	}
	if got := decode[lotBody](t, payload); got.HourlyRate == nil || *got.HourlyRate != 5000 {
		t.Errorf("get hourlyRate = %v, want 5000", got.HourlyRate)
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/lots", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", resp.StatusCode)
	}
	lots := decode[[]lotBody](t, payload)
	if len(lots) != 1 || lots[0].HourlyRate == nil || *lots[0].HourlyRate != 5000 {
		t.Errorf("lots = %+v, want the created lot with its rate", lots)
	}

	// A lot created without a rate carries an explicit null.
	resp, payload = request(t, server, http.MethodPost, "/api/v1/lots", map[string]any{
		"name":     "Free Plaza",
		"address":  "MG Road",
		"capacity": 1,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create lot without a rate status = %d, body = %s", resp.StatusCode, payload)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode response %q: %v", payload, err)
	}
	value, ok := raw["hourlyRate"]
	if !ok {
		t.Fatalf("response %s has no hourlyRate", payload)
	}
	if string(value) != "null" {
		t.Errorf("hourlyRate = %s, want null", value)
	}

	// Zero is a valid rate and means free parking.
	resp, payload = request(t, server, http.MethodPost, "/api/v1/lots", map[string]any{
		"name":       "Zero Plaza",
		"address":    "MG Road",
		"capacity":   1,
		"hourlyRate": 0,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create lot with a zero rate status = %d, body = %s", resp.StatusCode, payload)
	}
	if free := decode[lotBody](t, payload); free.HourlyRate == nil || *free.HourlyRate != 0 {
		t.Errorf("hourlyRate = %v, want 0", free.HourlyRate)
	}

	// A negative rate is rejected and stores nothing.
	resp, payload = request(t, server, http.MethodPost, "/api/v1/lots", map[string]any{
		"name":       "Negative Plaza",
		"address":    "MG Road",
		"capacity":   1,
		"hourlyRate": -1,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative rate status = %d, want 400, body = %s", resp.StatusCode, payload)
	}
	body := decode[problemBody](t, payload)
	if body.Code != "VALIDATION_FAILED" {
		t.Errorf("code = %q, want VALIDATION_FAILED", body.Code)
	}
	if !strings.Contains(body.Detail, "hourlyRate") {
		t.Errorf("detail = %q, want it to name hourlyRate", body.Detail)
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/lots", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", resp.StatusCode)
	}
	if lots = decode[[]lotBody](t, payload); len(lots) != 3 {
		t.Errorf("lots = %d, want the three accepted lots", len(lots))
	}
}

func TestCheckInAndCheckout(t *testing.T) {
	server := newTestServer(t)
	lot := createLot(t, server, 1)

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
		"plate": "ka 01 ab 1234",
		"kind":  "EV",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("check-in status = %d, body = %s", resp.StatusCode, payload)
	}
	session := decode[sessionBody](t, payload)
	if session.Plate != "KA 01 AB 1234" {
		t.Errorf("plate = %q, want an upper-cased plate", session.Plate)
	}
	if session.Kind != "EV" || session.Status != "ACTIVE" {
		t.Errorf("session = %+v, want an active EV session", session)
	}
	if location := resp.Header.Get("Location"); location != "/api/v1/sessions/"+session.ID {
		t.Errorf("Location = %q, want the session URL", location)
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/sessions/"+session.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d, want 200", resp.StatusCode)
	}

	resp, payload = request(t, server, http.MethodPost, "/api/v1/sessions/"+session.ID+"/checkout", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("checkout status = %d, body = %s", resp.StatusCode, payload)
	}
	checkedOut := decode[sessionBody](t, payload)
	if checkedOut.Status != "CHECKED_OUT" || checkedOut.CheckedOutAt == nil {
		t.Errorf("session = %+v, want a checked-out session with a timestamp", checkedOut)
	}

	resp, payload = request(t, server, http.MethodPost, "/api/v1/sessions/"+session.ID+"/checkout", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second checkout status = %d, want 409", resp.StatusCode)
	}
	if got := decode[problemBody](t, payload).Code; got != "SESSION_NOT_ACTIVE" {
		t.Errorf("code = %q, want SESSION_NOT_ACTIVE", got)
	}
}

func TestCheckInRejectsFullLot(t *testing.T) {
	server := newTestServer(t)
	lot := createLot(t, server, 1)

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
		"plate": "KA 01 AB 1234",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first check-in status = %d, body = %s", resp.StatusCode, payload)
	}

	resp, payload = request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
		"plate": "KA 02 CD 5678",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second check-in status = %d, want 409", resp.StatusCode)
	}
	if got := decode[problemBody](t, payload).Code; got != "LOT_FULL" {
		t.Errorf("code = %q, want LOT_FULL", got)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", contentType)
	}
}

func TestValidationErrorsAreProblemResponses(t *testing.T) {
	server := newTestServer(t)

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots", map[string]any{
		"name":     "Central Plaza",
		"address":  "MG Road",
		"capacity": 0,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decode[problemBody](t, payload)
	if body.Code != "VALIDATION_FAILED" {
		t.Errorf("code = %q, want VALIDATION_FAILED", body.Code)
	}
	if body.Status != http.StatusBadRequest || body.Instance != "/api/v1/lots" {
		t.Errorf("problem = %+v, want status and instance filled in", body)
	}
}

func TestUnknownRoutesReturnProblemJSON(t *testing.T) {
	server := newTestServer(t)

	resp, payload := request(t, server, http.MethodGet, "/api/v1/unknown", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if got := decode[problemBody](t, payload).Code; got != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND", got)
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/lots/missing", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing lot status = %d, want 404", resp.StatusCode)
	}
	if got := decode[problemBody](t, payload).Code; got != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND", got)
	}
}

func TestCheckInWithPhoneSendsConfirmation(t *testing.T) {
	notifier := &recordingNotifier{}
	server := newTestServerWithNotifier(t, notifier)
	lot := createLot(t, server, 1)

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
		"plate": "KA 01 AB 1234",
		"phone": "+14155550123",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("check-in status = %d, body = %s", resp.StatusCode, payload)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("decode response %q: %v", payload, err)
	}
	if _, ok := body["phone"]; ok {
		t.Errorf("response %s exposes the phone number", payload)
	}

	if len(notifier.messages) != 1 {
		t.Fatalf("messages = %d, want exactly one", len(notifier.messages))
	}
	msg := notifier.messages[0]
	if msg.To != "+14155550123" {
		t.Errorf("to = %q, want the submitted phone", msg.To)
	}
	if !strings.Contains(msg.Body, "Central Plaza") || !strings.Contains(msg.Body, "KA 01 AB 1234") {
		t.Errorf("body = %q, want the lot and the plate", msg.Body)
	}
}

func TestCheckInWithoutPhoneSendsNothing(t *testing.T) {
	notifier := &recordingNotifier{}
	server := newTestServerWithNotifier(t, notifier)
	lot := createLot(t, server, 1)

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
		"plate": "KA 01 AB 1234",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("check-in status = %d, body = %s", resp.StatusCode, payload)
	}
	if len(notifier.messages) != 0 {
		t.Errorf("messages = %+v, want none", notifier.messages)
	}
}

func TestCheckInRejectsInvalidPhone(t *testing.T) {
	notifier := &recordingNotifier{}
	server := newTestServerWithNotifier(t, notifier)
	lot := createLot(t, server, 1)

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
		"plate": "KA 01 AB 1234",
		"phone": "0415 555 0123",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", resp.StatusCode, payload)
	}
	if got := decode[problemBody](t, payload).Code; got != "VALIDATION_FAILED" {
		t.Errorf("code = %q, want VALIDATION_FAILED", got)
	}
	if len(notifier.messages) != 0 {
		t.Fatalf("messages = %+v, want none", notifier.messages)
	}

	// The lot holds one vehicle, so a successful check-in proves the rejected
	// request created no session.
	resp, payload = request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
		"plate": "KA 02 CD 5678",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("check-in after the rejected phone = %d, body = %s", resp.StatusCode, payload)
	}
}

func TestCheckoutReturnsPendingPayment(t *testing.T) {
	server := newTestServer(t)
	lot, session := checkoutAndPay(t, server, 1)

	if session.Payment == nil {
		t.Fatalf("checkout response %+v has no payment", session)
	}
	created := *session.Payment
	if created.SessionID != session.ID || created.LotID != lot.ID {
		t.Errorf("payment = %+v, want the session %q and lot %q", created, session.ID, lot.ID)
	}
	if created.Amount != testHourlyRate {
		t.Errorf("amount = %d, want the one-hour minimum %d", created.Amount, testHourlyRate)
	}
	if created.Currency != "INR" {
		t.Errorf("currency = %q, want INR", created.Currency)
	}
	if created.Provider != payment.FakeProviderName {
		t.Errorf("provider = %q, want %q", created.Provider, payment.FakeProviderName)
	}
	if created.Status != "PENDING" {
		t.Errorf("status = %q, want PENDING", created.Status)
	}

	// Reading the session keeps its body: only checkout carries the payment.
	resp, payload := request(t, server, http.MethodGet, "/api/v1/sessions/"+session.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d, want 200", resp.StatusCode)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("decode response %q: %v", payload, err)
	}
	if _, ok := body["payment"]; ok {
		t.Errorf("get session response %s carries a payment", payload)
	}
}

func TestGetPaymentReturnsStatusAndTimestamps(t *testing.T) {
	server := newTestServer(t)
	_, session := checkoutAndPay(t, server, 1)
	if session.Payment == nil {
		t.Fatalf("checkout response %+v has no payment", session)
	}

	resp, payload := request(t, server, http.MethodGet, "/api/v1/payments/"+session.Payment.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get payment status = %d, body = %s", resp.StatusCode, payload)
	}
	stored := decode[paymentBody](t, payload)
	if stored.ID != session.Payment.ID {
		t.Errorf("payment id = %q, want %q", stored.ID, session.Payment.ID)
	}
	if stored.Status != "PENDING" {
		t.Errorf("status = %q, want PENDING", stored.Status)
	}
	if stored.Amount != testHourlyRate || stored.Currency != "INR" {
		t.Errorf("payment = %+v, want %d minor units of INR", stored, testHourlyRate)
	}
	if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Errorf("payment = %+v, want both timestamps", stored)
	}
	if stored.ClientSecret != "" || stored.FailureMessage != "" {
		t.Errorf("payment = %+v, want no client secret or failure message from the fake provider", stored)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode response %q: %v", payload, err)
	}
	for _, field := range []string{"clientSecret", "failureMessage"} {
		if _, ok := raw[field]; ok {
			t.Errorf("response %s carries %q, want it omitted when empty", payload, field)
		}
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/payments/missing", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown payment status = %d, want 404, body = %s", resp.StatusCode, payload)
	}
	if got := decode[problemBody](t, payload).Code; got != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND", got)
	}
}

func TestWebhookMovesPaymentToPaidThenConflicts(t *testing.T) {
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
		t.Fatalf("webhook status = %d, body = %s", resp.StatusCode, payload)
	}
	if paid := decode[paymentBody](t, payload); paid.Status != "PAID" {
		t.Errorf("status = %q, want PAID", paid.Status)
	}

	resp, payload = request(t, server, http.MethodPost, "/api/v1/payments/webhooks/"+payment.FakeProviderName, map[string]any{
		"paymentId": session.Payment.ID,
		"status":    "FAILED",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second webhook status = %d, want 409, body = %s", resp.StatusCode, payload)
	}
	body := decode[problemBody](t, payload)
	if body.Code != "PAYMENT_NOT_PENDING" {
		t.Errorf("code = %q, want PAYMENT_NOT_PENDING", body.Code)
	}
	if body.Type == "" || body.Title == "" || body.Detail == "" || body.Status != http.StatusConflict {
		t.Errorf("problem = %+v, want the full problem+json envelope", body)
	}
	if body.Instance != "/api/v1/payments/webhooks/"+payment.FakeProviderName {
		t.Errorf("instance = %q, want the webhook path", body.Instance)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", contentType)
	}
}

func TestWebhookRejectsUnknownProvider(t *testing.T) {
	server := newTestServer(t)
	_, session := checkoutAndPay(t, server, 1)
	if session.Payment == nil {
		t.Fatalf("checkout response %+v has no payment", session)
	}

	resp, payload := request(t, server, http.MethodPost, "/api/v1/payments/webhooks/other", map[string]any{
		"paymentId": session.Payment.ID,
		"status":    "PAID",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", resp.StatusCode, payload)
	}
	if got := decode[problemBody](t, payload).Code; got != "VALIDATION_FAILED" {
		t.Errorf("code = %q, want VALIDATION_FAILED", got)
	}
}

func TestCheckoutTwiceReturnsOnePayment(t *testing.T) {
	server, payments := newTestServerWithStores(t, nil)
	ctx := context.Background()

	lot := createLot(t, server, 1)
	session := checkInVehicle(t, server, lot.ID, "KA 01 AB 1234")

	resp, payload := request(t, server, http.MethodPost, "/api/v1/sessions/"+session.ID+"/checkout", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("checkout status = %d, body = %s", resp.StatusCode, payload)
	}
	first := decode[sessionBody](t, payload)
	if first.Payment == nil {
		t.Fatalf("checkout response %+v has no payment", first)
	}

	resp, payload = request(t, server, http.MethodPost, "/api/v1/sessions/"+session.ID+"/checkout", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second checkout status = %d, want 409, body = %s", resp.StatusCode, payload)
	}
	if got := decode[problemBody](t, payload).Code; got != "SESSION_NOT_ACTIVE" {
		t.Errorf("code = %q, want SESSION_NOT_ACTIVE", got)
	}

	// The second checkout is a conflict and never reaches payment creation, so
	// the session keeps exactly the one payment the first checkout made.
	stored, err := payments.GetBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get payment by session: %v", err)
	}
	if stored.ID != first.Payment.ID {
		t.Errorf("payment id = %q, want the only payment %q", stored.ID, first.Payment.ID)
	}
}

// TestPaymentResponseCarriesOptionalProviderDetails seeds a payment directly,
// because no route can set a client secret or a failure message yet, and reads
// it back to assert the wire shape of the two optional fields.
func TestPaymentResponseCarriesOptionalProviderDetails(t *testing.T) {
	server, payments := newTestServerWithStores(t, nil)
	ctx := context.Background()
	now := time.Date(2026, time.November, 17, 9, 0, 0, 0, time.UTC)

	pending := domain.Payment{
		ID:           "pay_pending",
		SessionID:    "ses_pending",
		LotID:        "lot_seeded",
		Amount:       testHourlyRate,
		Currency:     domain.CurrencyINR,
		Provider:     "stripe",
		Status:       domain.PaymentPending,
		ClientSecret: "cs_test_1",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	failed := domain.Payment{
		ID:             "pay_failed",
		SessionID:      "ses_failed",
		LotID:          "lot_seeded",
		Amount:         testHourlyRate,
		Currency:       domain.CurrencyINR,
		Provider:       "stripe",
		Status:         domain.PaymentFailed,
		FailureMessage: "Your card was declined.",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	for _, seeded := range []domain.Payment{pending, failed} {
		if err := payments.Create(ctx, seeded); err != nil {
			t.Fatalf("seed payment %s: %v", seeded.ID, err)
		}
	}

	resp, payload := request(t, server, http.MethodGet, "/api/v1/payments/"+pending.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get pending payment status = %d, body = %s", resp.StatusCode, payload)
	}
	got := decode[paymentBody](t, payload)
	if got.ClientSecret != "cs_test_1" {
		t.Errorf("clientSecret = %q, want %q", got.ClientSecret, "cs_test_1")
	}
	if got.FailureMessage != "" {
		t.Errorf("failureMessage = %q, want it empty while the payment is pending", got.FailureMessage)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode response %q: %v", payload, err)
	}
	secret, ok := raw["clientSecret"]
	if !ok || string(secret) != `"cs_test_1"` {
		t.Errorf("clientSecret = %s, want \"cs_test_1\"", secret)
	}
	if value, ok := raw["failureMessage"]; ok {
		t.Errorf("response carries failureMessage = %s, want it omitted when empty", value)
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/payments/"+failed.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get failed payment status = %d, body = %s", resp.StatusCode, payload)
	}
	if got := decode[paymentBody](t, payload); got.FailureMessage != "Your card was declined." {
		t.Errorf("failureMessage = %q, want the stored failure message", got.FailureMessage)
	}

	raw = nil
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode response %q: %v", payload, err)
	}
	message, ok := raw["failureMessage"]
	if !ok || string(message) != `"Your card was declined."` {
		t.Errorf("failureMessage = %s, want \"Your card was declined.\"", message)
	}
	if value, ok := raw["clientSecret"]; ok {
		t.Errorf("response carries clientSecret = %s, want it omitted when empty", value)
	}
}

// TestCheckoutProviderFailureReturnsBadGateway covers a provider timeout or 5xx
// during checkout: the handler answers 502 PROVIDER_ERROR, the session stays
// checked out and the payment it created stays PENDING.
func TestCheckoutProviderFailureReturnsBadGateway(t *testing.T) {
	server, payments := newTestServerWithProvider(t, nil, unavailableProvider{})
	lot := createLot(t, server, 1)
	session := checkInVehicle(t, server, lot.ID, "KA 01 AB 1234")

	resp, payload := request(t, server, http.MethodPost, "/api/v1/sessions/"+session.ID+"/checkout", nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("checkout status = %d, want 502, body = %s", resp.StatusCode, payload)
	}
	body := decode[problemBody](t, payload)
	if body.Code != "PROVIDER_ERROR" {
		t.Errorf("code = %q, want PROVIDER_ERROR", body.Code)
	}
	if body.Status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", body.Status)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", contentType)
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/sessions/"+session.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d, body = %s", resp.StatusCode, payload)
	}
	stored := decode[sessionBody](t, payload)
	if stored.Status != "CHECKED_OUT" || stored.CheckedOutAt == nil {
		t.Errorf("session = %+v, want a checked-out session with a timestamp", stored)
	}

	pending, err := payments.GetBySession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("get payment by session: %v", err)
	}
	if pending.Status != domain.PaymentPending {
		t.Errorf("payment status = %q, want %q", pending.Status, domain.PaymentPending)
	}
}
