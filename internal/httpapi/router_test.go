package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/httpapi"
	"github.com/techverito/parking-service/internal/service"
	"github.com/techverito/parking-service/internal/store/memory"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	return newTestServerWithNotifier(t, nil)
}

func newTestServerWithNotifier(t *testing.T, notifier service.Notifier) *httptest.Server {
	t.Helper()

	lots := memory.NewLotStore()
	sessions := memory.NewSessionStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	server := httptest.NewServer(httpapi.NewRouter(
		service.NewLotService(lots, time.Now),
		service.NewSessionService(lots, sessions, notifier, logger, time.Now),
		logger,
	))
	t.Cleanup(server.Close)
	return server
}

// fakeNotifier records the check-in confirmations the service tried to send, so
// the HTTP tests can assert on them without a provider.
type fakeNotifier struct {
	mu      sync.Mutex
	calls   int
	notices []domain.CheckInNotice
	err     error
}

func (n *fakeNotifier) NotifyCheckIn(_ context.Context, notice domain.CheckInNotice) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	n.notices = append(n.notices, notice)
	return n.err
}

func (n *fakeNotifier) callCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls
}

func (n *fakeNotifier) lastNotice() (domain.CheckInNotice, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.notices) == 0 {
		return domain.CheckInNotice{}, false
	}
	return n.notices[len(n.notices)-1], true
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
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	Capacity  int       `json:"capacity"`
	CreatedAt time.Time `json:"createdAt"`
}

type sessionBody struct {
	ID           string     `json:"id"`
	LotID        string     `json:"lotId"`
	Plate        string     `json:"plate"`
	Phone        string     `json:"phone"`
	Kind         string     `json:"kind"`
	Status       string     `json:"status"`
	CheckedInAt  time.Time  `json:"checkedInAt"`
	CheckedOutAt *time.Time `json:"checkedOutAt"`
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

func TestCheckInWithPhoneReturnsIt(t *testing.T) {
	server := newTestServer(t)
	lot := createLot(t, server, 1)

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
		"plate": "KA 01 AB 1234",
		"phone": "+14155552671",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("check-in status = %d, body = %s", resp.StatusCode, payload)
	}
	checkedIn := decode[sessionBody](t, payload)
	if checkedIn.Phone != "+14155552671" {
		t.Errorf("phone = %q, want the submitted number", checkedIn.Phone)
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/sessions/"+checkedIn.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d, want 200", resp.StatusCode)
	}
	if got := decode[sessionBody](t, payload).Phone; got != "+14155552671" {
		t.Errorf("read back phone = %q, want the submitted number", got)
	}

	// A check-in without a phone leaves the key out of the session body.
	another := createLot(t, server, 1)
	resp, payload = request(t, server, http.MethodPost, "/api/v1/lots/"+another.ID+"/check-ins", map[string]any{
		"plate": "KA 02 CD 5678",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("check-in without phone status = %d, body = %s", resp.StatusCode, payload)
	}
	if bytes.Contains(payload, []byte(`"phone"`)) {
		t.Errorf("body = %s, want no phone key", payload)
	}
	if got := decode[sessionBody](t, payload).Phone; got != "" {
		t.Errorf("phone = %q, want it empty", got)
	}
}

func TestCheckInRejectsInvalidPhone(t *testing.T) {
	cases := []struct {
		name  string
		phone string
	}{
		{name: "missing plus", phone: "4155552671"},
		{name: "zero country code", phone: "+01155552671"},
		{name: "non digit", phone: "+1415555267a"},
		{name: "too many digits", phone: "+1415555267112345"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			lot := createLot(t, server, 1)

			resp, payload := request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
				"plate": "KA 01 AB 1234",
				"phone": tc.phone,
			})
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", resp.StatusCode, payload)
			}

			body := decode[problemBody](t, payload)
			if body.Code != "VALIDATION_FAILED" {
				t.Errorf("code = %q, want VALIDATION_FAILED", body.Code)
			}
			if body.Type == "" || body.Title == "" || body.Detail == "" || body.Instance == "" {
				t.Errorf("problem = %+v, want type, title, detail and instance filled in", body)
			}
			if body.Status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", body.Status)
			}

			// The rejected request created no session, so the single space in
			// the lot is still free.
			resp, payload = request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
				"plate": "KA 02 CD 5678",
			})
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("check-in after a rejected phone status = %d, want 201, body = %s", resp.StatusCode, payload)
			}
		})
	}
}

func TestCheckInNotifiesOnceAtHTTP(t *testing.T) {
	notifier := &fakeNotifier{}
	server := newTestServerWithNotifier(t, notifier)
	lot := createLot(t, server, 1)

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
		"plate": "ka 01 ab 1234",
		"phone": "+14155552671",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("check-in status = %d, body = %s", resp.StatusCode, payload)
	}
	session := decode[sessionBody](t, payload)

	if got := notifier.callCount(); got != 1 {
		t.Fatalf("notify calls = %d, want exactly one", got)
	}
	notice, ok := notifier.lastNotice()
	if !ok {
		t.Fatal("no notice was recorded")
	}
	if notice.SessionID != session.ID {
		t.Errorf("session id = %q, want %q", notice.SessionID, session.ID)
	}
	if notice.LotName != lot.Name {
		t.Errorf("lot name = %q, want %q", notice.LotName, lot.Name)
	}
	if notice.Plate != "KA 01 AB 1234" {
		t.Errorf("plate = %q, want the normalised plate", notice.Plate)
	}
	if notice.Phone != "+14155552671" {
		t.Errorf("phone = %q, want the submitted number", notice.Phone)
	}
	if !notice.CheckedInAt.Equal(session.CheckedInAt) {
		t.Errorf("checkedInAt = %v, want %v", notice.CheckedInAt, session.CheckedInAt)
	}

	// A check-in without a phone sends nothing.
	another := createLot(t, server, 1)
	resp, payload = request(t, server, http.MethodPost, "/api/v1/lots/"+another.ID+"/check-ins", map[string]any{
		"plate": "KA 02 CD 5678",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("check-in without phone status = %d, body = %s", resp.StatusCode, payload)
	}
	if got := notifier.callCount(); got != 1 {
		t.Errorf("notify calls = %d, want no notification without a phone", got)
	}
}

func TestCheckInSucceedsWhenProviderFails(t *testing.T) {
	notifier := &fakeNotifier{err: errors.New("twilio rejected the message")}
	server := newTestServerWithNotifier(t, notifier)
	lot := createLot(t, server, 1)

	resp, payload := request(t, server, http.MethodPost, "/api/v1/lots/"+lot.ID+"/check-ins", map[string]any{
		"plate": "KA 01 AB 1234",
		"phone": "+14155552671",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("check-in status = %d, body = %s", resp.StatusCode, payload)
	}
	session := decode[sessionBody](t, payload)

	if got := notifier.callCount(); got != 1 {
		t.Errorf("notify calls = %d, want exactly one attempt", got)
	}

	resp, payload = request(t, server, http.MethodGet, "/api/v1/sessions/"+session.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d, want 200, body = %s", resp.StatusCode, payload)
	}
	if got := decode[sessionBody](t, payload); got.ID != session.ID {
		t.Errorf("session id = %q, want %q", got.ID, session.ID)
	}
}
