package service_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/notify"
	"github.com/techverito/parking-service/internal/payment"
	"github.com/techverito/parking-service/internal/payments"
	"github.com/techverito/parking-service/internal/service"
	"github.com/techverito/parking-service/internal/store/memory"
)

// defaultTestHourlyRate is the PAYMENT_HOURLY_RATE the services are wired with.
const defaultTestHourlyRate = 5000

// newHarness wires the services onto in-memory stores with the fake payment
// provider and returns the payment store too, so a test can assert which
// payments a session received.
func newHarness(t *testing.T, msgNotifier notify.Notifier, logger *slog.Logger) (*service.LotService, *service.SessionService, *memory.PaymentStore, domain.Lot) {
	t.Helper()

	fixed := time.Date(2026, time.November, 17, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return fixed }

	lots := memory.NewLotStore()
	sessions := memory.NewSessionStore()
	paymentStore := memory.NewPaymentStore()
	lotService := service.NewLotService(lots, clock)

	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	provider, err := payments.FromSettings(payments.Settings{Name: payment.FakeProviderName})
	if err != nil {
		t.Fatalf("build payment provider: %v", err)
	}
	paymentService := service.NewPaymentService(paymentStore, lots, provider, defaultTestHourlyRate, clock)
	sessionService := service.NewSessionService(lots, sessions, paymentService, clock, msgNotifier, logger)

	lot, err := lotService.Create(context.Background(), service.CreateLotInput{
		Name:     "Central Plaza",
		Address:  "MG Road",
		Capacity: 1,
	})
	if err != nil {
		t.Fatalf("create lot: %v", err)
	}
	return lotService, sessionService, paymentStore, lot
}

func newServices(t *testing.T) (*service.LotService, *service.SessionService, domain.Lot) {
	t.Helper()

	lotService, sessionService, _, lot := newHarness(t, nil, nil)
	return lotService, sessionService, lot
}

// recordingNotifier records what the service tries to send and returns the
// configured error, so notification behaviour is asserted without a provider.
type recordingNotifier struct {
	messages []notify.Message
	err      error
}

func (n *recordingNotifier) Send(_ context.Context, msg notify.Message) error {
	n.messages = append(n.messages, msg)
	return n.err
}

// unavailableProvider is a payment.Provider whose CreatePayment always reports
// the provider as unreachable. It counts its calls, so a test can prove the
// checkout reached the provider exactly once.
type unavailableProvider struct {
	createCalls int
}

func (p *unavailableProvider) Name() string { return payment.FakeProviderName }

func (p *unavailableProvider) CreatePayment(_ context.Context, _ payment.CreateRequest) (payment.CreateResult, error) {
	p.createCalls++
	return payment.CreateResult{}, fmt.Errorf("provider is down: %w", domain.ErrProviderUnavailable)
}

func (p *unavailableProvider) ParseWebhook(_ context.Context, _ payment.WebhookRequest) (payment.WebhookEvent, error) {
	return payment.WebhookEvent{}, errors.New("parse webhook is not used by this stub")
}

// newServicesWithNotifier wires the services with the given notifier and an
// info-level logger writing to the returned buffer.
func newServicesWithNotifier(t *testing.T, notifier notify.Notifier) (*service.SessionService, *bytes.Buffer, domain.Lot) {
	t.Helper()

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))

	_, sessionService, _, lot := newHarness(t, notifier, logger)
	return sessionService, logs, lot
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

func TestCheckoutTwiceReturnsTheSamePayment(t *testing.T) {
	_, sessions, payments, lot := newHarness(t, nil, nil)
	ctx := context.Background()

	session, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{Plate: "KA 01 AB 1234"})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}

	checkedOut, pending, err := sessions.Checkout(ctx, session.ID)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if checkedOut.Status != domain.SessionCheckedOut {
		t.Errorf("status = %q, want %q", checkedOut.Status, domain.SessionCheckedOut)
	}
	if checkedOut.CheckedOutAt == nil {
		t.Fatal("checkedOutAt is nil")
	}
	if pending.Status != domain.PaymentPending {
		t.Errorf("payment status = %q, want %q", pending.Status, domain.PaymentPending)
	}

	// A repeated checkout is idempotent: it answers with the payment the first
	// checkout made instead of failing.
	again, second, err := sessions.Checkout(ctx, session.ID)
	if err != nil {
		t.Fatalf("second checkout: %v", err)
	}
	if again.Status != domain.SessionCheckedOut {
		t.Errorf("status = %q, want %q", again.Status, domain.SessionCheckedOut)
	}
	if second.ID != pending.ID {
		t.Errorf("payment id = %q, want the first payment %q", second.ID, pending.ID)
	}
	if second.Status != domain.PaymentPending {
		t.Errorf("payment status = %q, want %q", second.Status, domain.PaymentPending)
	}

	// The session keeps exactly the one payment the first checkout made.
	stored, err := payments.GetBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get payment by session: %v", err)
	}
	if stored.ID != pending.ID {
		t.Errorf("payment id = %q, want the only payment %q", stored.ID, pending.ID)
	}
}

func TestCheckoutStaysCheckedOutWhenTheProviderFails(t *testing.T) {
	fixed := time.Date(2026, time.November, 17, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return fixed }

	lots := memory.NewLotStore()
	sessions := memory.NewSessionStore()
	paymentStore := memory.NewPaymentStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	provider := &unavailableProvider{}

	paymentService := service.NewPaymentService(paymentStore, lots, provider, defaultTestHourlyRate, clock)
	sessionService := service.NewSessionService(lots, sessions, paymentService, clock, nil, logger)
	ctx := context.Background()

	lot, err := service.NewLotService(lots, clock).Create(ctx, service.CreateLotInput{
		Name:     "Central Plaza",
		Address:  "MG Road",
		Capacity: 1,
	})
	if err != nil {
		t.Fatalf("create lot: %v", err)
	}
	session, err := sessionService.CheckIn(ctx, lot.ID, service.CheckInInput{Plate: "KA 01 AB 1234"})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}

	if _, _, err := sessionService.Checkout(ctx, session.ID); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("checkout error = %v, want ErrProviderUnavailable", err)
	}

	// The session is written before the payment is opened, so the vehicle stays
	// checked out with the PENDING payment the attempt persisted.
	stored, err := sessionService.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.Status != domain.SessionCheckedOut {
		t.Errorf("status = %q, want %q after a provider failure", stored.Status, domain.SessionCheckedOut)
	}
	if stored.CheckedOutAt == nil {
		t.Fatal("checkedOutAt is nil")
	}

	pending, err := paymentStore.GetBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get payment by session: %v", err)
	}
	if pending.Status != domain.PaymentPending {
		t.Errorf("payment status = %q, want %q", pending.Status, domain.PaymentPending)
	}

	// A retried checkout returns the same PENDING payment without reaching the
	// provider again, so the retry can neither charge twice nor open a second
	// order.
	retried, retriedPayment, err := sessionService.Checkout(ctx, session.ID)
	if err != nil {
		t.Fatalf("retried checkout: %v", err)
	}
	if retried.Status != domain.SessionCheckedOut {
		t.Errorf("retried session status = %q, want %q", retried.Status, domain.SessionCheckedOut)
	}
	if retriedPayment.ID != pending.ID {
		t.Errorf("retried payment id = %q, want the only payment %q", retriedPayment.ID, pending.ID)
	}
	if retriedPayment.Status != domain.PaymentPending {
		t.Errorf("retried payment status = %q, want %q", retriedPayment.Status, domain.PaymentPending)
	}
	if provider.createCalls != 1 {
		t.Errorf("provider calls = %d, want exactly one", provider.createCalls)
	}
	again, err := paymentStore.GetBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get payment by session after the retry: %v", err)
	}
	if again.ID != pending.ID {
		t.Errorf("payment id = %q, want the only payment %q", again.ID, pending.ID)
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

func TestCheckInSendsConfirmationWhenPhoneIsPresent(t *testing.T) {
	notifier := &recordingNotifier{}
	sessions, _, lot := newServicesWithNotifier(t, notifier)

	if _, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{
		Plate: "ka 01 ab 1234",
		Phone: "+14155550123",
	}); err != nil {
		t.Fatalf("check in: %v", err)
	}

	if len(notifier.messages) != 1 {
		t.Fatalf("messages = %d, want exactly one", len(notifier.messages))
	}
	msg := notifier.messages[0]
	if msg.To != "+14155550123" {
		t.Errorf("to = %q, want %q", msg.To, "+14155550123")
	}
	want := "Checked in to Central Plaza. Plate KA 01 AB 1234. Start 2026-11-17T09:00:00Z."
	if msg.Body != want {
		t.Errorf("body = %q, want %q", msg.Body, want)
	}
}

func TestCheckInWithoutPhoneSkipsNotification(t *testing.T) {
	notifier := &recordingNotifier{}
	sessions, _, lot := newServicesWithNotifier(t, notifier)

	session, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{Plate: "KA 01 AB 1234"})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}
	if session.Status != domain.SessionActive {
		t.Errorf("status = %q, want %q", session.Status, domain.SessionActive)
	}
	if len(notifier.messages) != 0 {
		t.Errorf("messages = %+v, want none", notifier.messages)
	}
}

func TestCheckInRejectsInvalidPhoneBeforeCreatingSession(t *testing.T) {
	cases := []struct {
		name  string
		phone string
	}{
		{name: "national format", phone: "0415 555 0123"},
		{name: "zero after plus", phone: "+0123456789"},
		{name: "not a number", phone: "abc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notifier := &recordingNotifier{}
			sessions, _, lot := newServicesWithNotifier(t, notifier)
			ctx := context.Background()

			_, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{
				Plate: "KA 01 AB 1234",
				Phone: tc.phone,
			})

			var validation *domain.ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
			if validation.Field != "phone" {
				t.Errorf("field = %q, want phone", validation.Field)
			}
			if len(notifier.messages) != 0 {
				t.Errorf("messages = %+v, want none", notifier.messages)
			}

			// The lot holds one vehicle, so a successful check-in proves the
			// rejected request created no session.
			session, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{Plate: "KA 02 CD 5678"})
			if err != nil {
				t.Fatalf("check in after the rejected phone: %v", err)
			}
			if session.Status != domain.SessionActive {
				t.Errorf("status = %q, want %q", session.Status, domain.SessionActive)
			}
		})
	}
}

func TestCheckInSurvivesNotifierFailure(t *testing.T) {
	notifier := &recordingNotifier{err: errors.New("twilio rejected the message")}
	sessions, logs, lot := newServicesWithNotifier(t, notifier)
	ctx := context.Background()

	session, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{
		Plate: "KA 01 AB 1234",
		Phone: "+14155550123",
	})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}
	if len(notifier.messages) != 1 {
		t.Fatalf("messages = %d, want exactly one attempt", len(notifier.messages))
	}

	stored, err := sessions.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.Status != domain.SessionActive {
		t.Errorf("status = %q, want %q", stored.Status, domain.SessionActive)
	}

	logged := logs.String()
	if !strings.Contains(logged, session.ID) {
		t.Errorf("log = %q, want the session id", logged)
	}
	if !strings.Contains(logged, "twilio rejected the message") {
		t.Errorf("log = %q, want the notifier error", logged)
	}
}
