package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/notify"
	"github.com/techverito/parking-service/internal/service"
	"github.com/techverito/parking-service/internal/store/memory"
)

// fakeNotifier records every message the service sends and can be told to fail
// the way a provider that rejects a request or times out does.
type fakeNotifier struct {
	messages []notify.Message
	err      error
}

func (n *fakeNotifier) Send(_ context.Context, message notify.Message) error {
	n.messages = append(n.messages, message)
	return n.err
}

// capturingHandler keeps every log record so a test can assert on what the
// service logged without reading stderr.
type capturingHandler struct {
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, record slog.Record) error {
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *capturingHandler) WithGroup(string) slog.Handler { return h }

func newServices(t *testing.T) (*service.LotService, *service.SessionService, domain.Lot) {
	t.Helper()

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newServicesWith(t, notify.NewLogNotifier(discard), discard)
}

func newServicesWithNotifier(t *testing.T, notifier notify.Notifier) (*service.SessionService, domain.Lot) {
	t.Helper()

	_, sessionService, lot := newServicesWith(t, notifier, nil)
	return sessionService, lot
}

// newServicesWith builds the lot and session services with an injected
// notifier and logger, so a test can observe what is sent and what is logged.
func newServicesWith(t *testing.T, notifier notify.Notifier, logger *slog.Logger) (*service.LotService, *service.SessionService, domain.Lot) {
	t.Helper()

	fixed := time.Date(2026, time.November, 17, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return fixed }

	lots := memory.NewLotStore()
	sessions := memory.NewSessionStore()
	lotService := service.NewLotService(lots, clock)
	sessionService := service.NewSessionService(lots, sessions, clock, notifier, logger)

	lot, err := lotService.Create(context.Background(), service.CreateLotInput{
		Name:     "Central Plaza",
		Address:  "MG Road",
		Capacity: 1,
	})
	if err != nil {
		t.Fatalf("create lot: %v", err)
	}
	return lotService, sessionService, lot
}

func TestCheckInNormalisesPlateAndDefaultsKind(t *testing.T) {
	_, sessions, lot := newServices(t)

	session, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{
		Plate: "  ka  01 ab 1234 ",
	})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}
	if session.Plate != "KA 01 AB 1234" {
		t.Errorf("plate = %q, want %q", session.Plate, "KA 01 AB 1234")
	}
	if session.Kind != domain.KindStandard {
		t.Errorf("kind = %q, want %q", session.Kind, domain.KindStandard)
	}
	if session.Status != domain.SessionActive {
		t.Errorf("status = %q, want %q", session.Status, domain.SessionActive)
	}
	if !session.CheckedInAt.Equal(time.Date(2026, time.November, 17, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("checkedInAt = %v, want the injected clock time", session.CheckedInAt)
	}
}

func TestCheckInRejectsInvalidPlate(t *testing.T) {
	_, sessions, lot := newServices(t)

	_, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{Plate: "!"})
	if err == nil {
		t.Fatal("expected a validation error")
	}

	var validation *domain.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if validation.Field != "plate" {
		t.Errorf("field = %q, want plate", validation.Field)
	}
}

func TestCheckInRejectsInvalidPhone(t *testing.T) {
	_, sessions, lot := newServices(t)
	ctx := context.Background()

	_, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{
		Plate: "KA 01 AB 1234",
		Phone: "4155552671",
	})
	if err == nil {
		t.Fatal("expected a validation error")
	}

	var validation *domain.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if validation.Field != "phone" {
		t.Errorf("field = %q, want phone", validation.Field)
	}

	// A rejected phone must not create a session, so the lot's only space is
	// still free for a valid check-in.
	if _, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{
		Plate: "KA 02 CD 5678",
		Phone: "+14155552671",
	}); err != nil {
		t.Fatalf("check in after a rejected phone: %v", err)
	}
}

func TestCheckInSendsOneConfirmation(t *testing.T) {
	notifier := &fakeNotifier{}
	sessions, lot := newServicesWithNotifier(t, notifier)

	session, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{
		Plate: "ka 01 ab 1234",
		Phone: "+14155552671",
	})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}
	if session.Phone != "+14155552671" {
		t.Errorf("phone = %q, want the normalized number", session.Phone)
	}

	if len(notifier.messages) != 1 {
		t.Fatalf("messages = %d, want exactly 1", len(notifier.messages))
	}
	message := notifier.messages[0]
	if message.To != "+14155552671" {
		t.Errorf("to = %q, want the session phone", message.To)
	}
	wants := []string{"Central Plaza", "KA 01 AB 1234", "2026-11-17T09:00:00Z"}
	for _, want := range wants {
		if !strings.Contains(message.Body, want) {
			t.Errorf("body = %q, want it to contain %q", message.Body, want)
		}
	}
	if length := utf8.RuneCountInString(message.Body); length > 160 {
		t.Errorf("body length = %d, want at most 160: %q", length, message.Body)
	}
}

func TestCheckInWithoutPhoneSendsNothing(t *testing.T) {
	notifier := &fakeNotifier{}
	sessions, lot := newServicesWithNotifier(t, notifier)

	session, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{
		Plate: "KA 01 AB 1234",
	})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}
	if session.ID == "" {
		t.Error("session has no id")
	}
	if session.Phone != "" {
		t.Errorf("phone = %q, want it to stay empty", session.Phone)
	}
	if len(notifier.messages) != 0 {
		t.Errorf("messages = %d, want none", len(notifier.messages))
	}
}

func TestCheckInWithNilNotifierUsesLogOnly(t *testing.T) {
	handler := &capturingHandler{}
	_, sessions, lot := newServicesWith(t, nil, slog.New(handler))

	session, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{
		Plate: "KA 01 AB 1234",
		Phone: "+14155552671",
	})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}
	if session.ID == "" {
		t.Error("session has no id")
	}

	if len(handler.records) != 1 {
		t.Fatalf("log records = %d, want the message logged exactly once", len(handler.records))
	}
	attributes := map[string]string{}
	handler.records[0].Attrs(func(attr slog.Attr) bool {
		attributes[attr.Key] = attr.Value.String()
		return true
	})
	if attributes["to"] != "+14155552671" {
		t.Errorf("logged to = %q, want the session phone", attributes["to"])
	}
	if !strings.Contains(attributes["body"], "KA 01 AB 1234") {
		t.Errorf("logged body = %q, want the confirmation text", attributes["body"])
	}
}

func TestCheckInSurvivesNotifierFailure(t *testing.T) {
	notifier := &fakeNotifier{err: context.DeadlineExceeded}
	handler := &capturingHandler{}
	_, sessions, lot := newServicesWith(t, notifier, slog.New(handler))
	ctx := context.Background()

	session, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{
		Plate: "KA 01 AB 1234",
		Phone: "+14155552671",
	})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}
	if len(notifier.messages) != 1 {
		t.Fatalf("messages = %d, want exactly 1 attempt", len(notifier.messages))
	}

	stored, err := sessions.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.Status != domain.SessionActive {
		t.Errorf("status = %q, want %q", stored.Status, domain.SessionActive)
	}

	if len(handler.records) != 1 {
		t.Fatalf("log records = %d, want exactly 1", len(handler.records))
	}
	attributes := map[string]string{}
	handler.records[0].Attrs(func(attr slog.Attr) bool {
		attributes[attr.Key] = attr.Value.String()
		return true
	})
	if attributes["session_id"] != session.ID {
		t.Errorf("logged session_id = %q, want %q", attributes["session_id"], session.ID)
	}
	if !strings.Contains(attributes["error"], context.DeadlineExceeded.Error()) {
		t.Errorf("logged error = %q, want the provider error", attributes["error"])
	}
}

func TestComposeCheckInMessageFitsOneSegment(t *testing.T) {
	notifier := &fakeNotifier{}
	lotService, sessions, _ := newServicesWith(t, notifier, nil)
	ctx := context.Background()

	longName := strings.Repeat("a", 200)
	lot, err := lotService.Create(ctx, service.CreateLotInput{
		Name:     longName,
		Address:  "MG Road",
		Capacity: 1,
	})
	if err != nil {
		t.Fatalf("create lot: %v", err)
	}

	if _, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{
		Plate: "KA 01 AB 1234",
		Phone: "+14155552671",
	}); err != nil {
		t.Fatalf("check in: %v", err)
	}

	if len(notifier.messages) != 1 {
		t.Fatalf("messages = %d, want exactly 1", len(notifier.messages))
	}
	body := notifier.messages[0].Body
	if length := utf8.RuneCountInString(body); length > 160 {
		t.Errorf("body length = %d, want at most 160: %q", length, body)
	}
	if !strings.Contains(body, "KA 01 AB 1234") || !strings.Contains(body, "2026-11-17T09:00:00Z") {
		t.Errorf("body = %q, want the plate and the start time", body)
	}
	if strings.Contains(body, longName) {
		t.Errorf("body = %q, want the lot name shortened", body)
	}
	lotSection, _, found := strings.Cut(body, ". Plate: ")
	if !found {
		t.Fatalf("body = %q, want a lot section followed by the plate", body)
	}
	if !strings.HasSuffix(lotSection, "...") {
		t.Errorf("lot section = %q, want the shortened name to end in \"...\"", lotSection)
	}
}

func TestComposeCheckInMessageKeepsGSM7(t *testing.T) {
	notifier := &fakeNotifier{}
	lotService, sessions, _ := newServicesWith(t, notifier, nil)
	ctx := context.Background()

	lot, err := lotService.Create(ctx, service.CreateLotInput{
		Name:     "Café Ω 中 Central",
		Address:  "MG Road",
		Capacity: 1,
	})
	if err != nil {
		t.Fatalf("create lot: %v", err)
	}

	if _, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{
		Plate: "KA 01 AB 1234",
		Phone: "+14155552671",
	}); err != nil {
		t.Fatalf("check in: %v", err)
	}

	if len(notifier.messages) != 1 {
		t.Fatalf("messages = %d, want exactly 1", len(notifier.messages))
	}
	body := notifier.messages[0].Body
	if strings.Contains(body, "中") {
		t.Errorf("body = %q, want characters outside GSM-7 dropped", body)
	}
	if !strings.Contains(body, "Café Ω") || !strings.Contains(body, "Central") {
		t.Errorf("body = %q, want the GSM-7 part of the lot name", body)
	}
}

func TestCheckInRejectsUnknownLot(t *testing.T) {
	_, sessions, _ := newServices(t)

	_, err := sessions.CheckIn(context.Background(), "missing", service.CheckInInput{Plate: "KA 01 AB 1234"})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestCheckInStopsWhenLotIsFull(t *testing.T) {
	_, sessions, lot := newServices(t)
	ctx := context.Background()

	if _, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{Plate: "KA 01 AB 1234"}); err != nil {
		t.Fatalf("first check in: %v", err)
	}

	_, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{Plate: "KA 02 CD 5678"})
	if !errors.Is(err, domain.ErrLotFull) {
		t.Fatalf("error = %v, want ErrLotFull", err)
	}
}

func TestCheckoutTwiceIsAConflict(t *testing.T) {
	_, sessions, lot := newServices(t)
	ctx := context.Background()

	session, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{Plate: "KA 01 AB 1234"})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}

	checkedOut, err := sessions.Checkout(ctx, session.ID)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if checkedOut.Status != domain.SessionCheckedOut {
		t.Errorf("status = %q, want %q", checkedOut.Status, domain.SessionCheckedOut)
	}
	if checkedOut.CheckedOutAt == nil {
		t.Fatal("checkedOutAt is nil")
	}

	if _, err := sessions.Checkout(ctx, session.ID); !errors.Is(err, domain.ErrSessionNotActive) {
		t.Fatalf("second checkout error = %v, want ErrSessionNotActive", err)
	}
}

func TestCreateLotValidatesInput(t *testing.T) {
	lotService := service.NewLotService(memory.NewLotStore(), nil)

	cases := []struct {
		name  string
		input service.CreateLotInput
		field string
	}{
		{name: "name required", input: service.CreateLotInput{Address: "MG Road", Capacity: 10}, field: "name"},
		{name: "address required", input: service.CreateLotInput{Name: "Central", Capacity: 10}, field: "address"},
		{name: "capacity required", input: service.CreateLotInput{Name: "Central", Address: "MG Road"}, field: "capacity"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := lotService.Create(context.Background(), tc.input)

			var validation *domain.ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
			if validation.Field != tc.field {
				t.Errorf("field = %q, want %q", validation.Field, tc.field)
			}
		})
	}
}
