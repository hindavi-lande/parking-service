package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/payment"
	"github.com/techverito/parking-service/internal/service"
	"github.com/techverito/parking-service/internal/store/memory"
)

// paymentClock is the injected clock the payment tests run at.
var paymentClock = time.Date(2026, time.November, 17, 9, 0, 0, 0, time.UTC)

// recordingProvider implements payment.Provider without any network call. It
// records what the service asked it to create and replays a configured webhook
// event.
type recordingProvider struct {
	name        string
	createCalls []payment.CreateRequest
	createErr   error
	event       payment.WebhookEvent
}

func (p *recordingProvider) Name() string {
	if p.name == "" {
		return payment.FakeProviderName
	}
	return p.name
}

func (p *recordingProvider) CreatePayment(_ context.Context, req payment.CreateRequest) error {
	p.createCalls = append(p.createCalls, req)
	return p.createErr
}

func (p *recordingProvider) ParseWebhook(_ context.Context, _ payment.WebhookRequest) (payment.WebhookEvent, error) {
	return p.event, nil
}

// newPaymentService wires a payment service with an in-memory store, an empty
// lot store and the fixed clock.
func newPaymentService(t *testing.T, provider payment.Provider, defaultHourlyRate int64) (*service.PaymentService, *memory.PaymentStore, *memory.LotStore) {
	t.Helper()

	payments := memory.NewPaymentStore()
	lots := memory.NewLotStore()
	clock := func() time.Time { return paymentClock }

	return service.NewPaymentService(payments, lots, provider, defaultHourlyRate, clock), payments, lots
}

// seededLot stores a lot with the given rate. A nil rate means the lot carries
// no rate of its own.
func seededLot(t *testing.T, lots *memory.LotStore, rate *int) domain.Lot {
	t.Helper()

	lot, err := service.NewLotService(lots, nil).Create(context.Background(), service.CreateLotInput{
		Name:       "Central Plaza",
		Address:    "MG Road",
		Capacity:   10,
		HourlyRate: rate,
	})
	if err != nil {
		t.Fatalf("create lot: %v", err)
	}
	return lot
}

// finishedSession is a checked-out session that started at the fixed clock and
// stayed for the given duration.
func finishedSession(id, lotID string, stay time.Duration) domain.Session {
	end := paymentClock.Add(stay)
	return domain.Session{
		ID:           id,
		LotID:        lotID,
		Plate:        "KA 01 AB 1234",
		Kind:         domain.KindStandard,
		Status:       domain.SessionCheckedOut,
		CheckedInAt:  paymentClock,
		CheckedOutAt: &end,
	}
}

func TestFeeAmountRoundsToStartedHoursWithMinimum(t *testing.T) {
	cases := []struct {
		name string
		stay time.Duration
		want int64
	}{
		{name: "one minute", stay: time.Minute, want: 5000},
		{name: "exactly one hour", stay: time.Hour, want: 5000},
		{name: "one hour and one second", stay: time.Hour + time.Second, want: 10_000},
		{name: "ninety minutes", stay: 90 * time.Minute, want: 10_000},
		{name: "zero duration", stay: 0, want: 5000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			amount, err := service.FeeAmount(finishedSession("ses_fee", "lot_fee", tc.stay), 5000)
			if err != nil {
				t.Fatalf("fee amount: %v", err)
			}
			if amount != tc.want {
				t.Errorf("amount = %d, want %d", amount, tc.want)
			}
		})
	}

	if _, err := service.FeeAmount(domain.Session{CheckedInAt: paymentClock}, 5000); err == nil {
		t.Error("expected an error for a session that has not been checked out")
	}
}

func TestCreateForCheckoutUsesLotRateOrDefault(t *testing.T) {
	cases := []struct {
		name string
		rate *int
		want int64
	}{
		{name: "lot rate wins", rate: intPtr(5000), want: 5000},
		{name: "zero is free parking", rate: intPtr(0), want: 0},
		{name: "lot without a rate uses the configured one", rate: nil, want: 2500},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &recordingProvider{}
			svc, payments, lots := newPaymentService(t, provider, 2500)
			lot := seededLot(t, lots, tc.rate)
			session := finishedSession("ses_rate", lot.ID, time.Hour)
			ctx := context.Background()

			created, err := svc.CreateForCheckout(ctx, session)
			if err != nil {
				t.Fatalf("create payment: %v", err)
			}
			if created.Amount != tc.want {
				t.Errorf("amount = %d, want %d", created.Amount, tc.want)
			}
			if created.Status != domain.PaymentPending {
				t.Errorf("status = %q, want %q", created.Status, domain.PaymentPending)
			}
			if created.Currency != domain.CurrencyINR {
				t.Errorf("currency = %q, want %q", created.Currency, domain.CurrencyINR)
			}
			if created.Provider != provider.Name() {
				t.Errorf("provider = %q, want %q", created.Provider, provider.Name())
			}
			if created.SessionID != session.ID || created.LotID != lot.ID {
				t.Errorf("payment = %+v, want the session and lot ids", created)
			}
			if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
				t.Errorf("payment = %+v, want both timestamps set", created)
			}

			stored, err := payments.Get(ctx, created.ID)
			if err != nil {
				t.Fatalf("get stored payment: %v", err)
			}
			if stored.Amount != tc.want || stored.Status != domain.PaymentPending {
				t.Errorf("stored payment = %+v, want %d minor units and PENDING", stored, tc.want)
			}

			if len(provider.createCalls) != 1 {
				t.Fatalf("provider calls = %d, want exactly one", len(provider.createCalls))
			}
			if provider.createCalls[0].Amount != tc.want {
				t.Errorf("provider amount = %d, want %d", provider.createCalls[0].Amount, tc.want)
			}
		})
	}
}

func TestCreateForCheckoutIsIdempotentPerSession(t *testing.T) {
	provider := &recordingProvider{}
	svc, payments, lots := newPaymentService(t, provider, 5000)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_duplicate", lot.ID, time.Hour)
	ctx := context.Background()

	first, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("first create payment: %v", err)
	}

	second, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("second create payment: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("payment id = %q, want the first payment %q", second.ID, first.ID)
	}
	if len(provider.createCalls) != 1 {
		t.Errorf("provider calls = %d, want exactly one", len(provider.createCalls))
	}

	stored, err := payments.GetBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get payment by session: %v", err)
	}
	if stored.ID != first.ID {
		t.Errorf("stored payment id = %q, want %q", stored.ID, first.ID)
	}
}

func TestCreateForCheckoutFailsWithoutStoringWhenProviderFails(t *testing.T) {
	provider := &recordingProvider{createErr: errors.New("provider rejected the payment")}
	svc, payments, lots := newPaymentService(t, provider, 5000)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_rejected", lot.ID, time.Hour)
	ctx := context.Background()

	if _, err := svc.CreateForCheckout(ctx, session); err == nil {
		t.Fatal("expected the provider error")
	}

	if _, err := payments.GetBySession(ctx, session.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("stored payment error = %v, want ErrNotFound", err)
	}
}

func TestApplyStatusRejectsTerminalTransition(t *testing.T) {
	cases := []struct {
		name  string
		first domain.PaymentStatus
		next  domain.PaymentStatus
	}{
		{name: "paid then failed", first: domain.PaymentPaid, next: domain.PaymentFailed},
		{name: "failed then paid", first: domain.PaymentFailed, next: domain.PaymentPaid},
		{name: "paid then paid", first: domain.PaymentPaid, next: domain.PaymentPaid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &recordingProvider{}
			svc, payments, lots := newPaymentService(t, provider, 5000)
			lot := seededLot(t, lots, nil)
			session := finishedSession("ses_terminal", lot.ID, time.Hour)
			ctx := context.Background()

			created, err := svc.CreateForCheckout(ctx, session)
			if err != nil {
				t.Fatalf("create payment: %v", err)
			}

			provider.event = payment.WebhookEvent{PaymentID: created.ID, Status: tc.first}
			settled, err := svc.HandleWebhook(ctx, provider.Name(), payment.WebhookRequest{})
			if err != nil {
				t.Fatalf("first webhook: %v", err)
			}
			if settled.Status != tc.first {
				t.Errorf("status = %q, want %q", settled.Status, tc.first)
			}
			if !settled.UpdatedAt.Equal(paymentClock) {
				t.Errorf("updatedAt = %v, want the injected clock time", settled.UpdatedAt)
			}

			provider.event = payment.WebhookEvent{PaymentID: created.ID, Status: tc.next}
			if _, err := svc.HandleWebhook(ctx, provider.Name(), payment.WebhookRequest{}); !errors.Is(err, domain.ErrPaymentNotPending) {
				t.Fatalf("second webhook error = %v, want ErrPaymentNotPending", err)
			}

			unchanged, err := payments.Get(ctx, created.ID)
			if err != nil {
				t.Fatalf("get payment: %v", err)
			}
			if unchanged.Status != tc.first {
				t.Errorf("status = %q, want %q to stay unchanged", unchanged.Status, tc.first)
			}

			provider.event = payment.WebhookEvent{PaymentID: "missing", Status: domain.PaymentPaid}
			if _, err := svc.HandleWebhook(ctx, provider.Name(), payment.WebhookRequest{}); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("unknown payment error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestHandleWebhookRejectsUnknownProvider(t *testing.T) {
	provider := &recordingProvider{name: payment.FakeProviderName}
	svc, _, _ := newPaymentService(t, provider, 5000)

	_, err := svc.HandleWebhook(context.Background(), "other", payment.WebhookRequest{})

	var validation *domain.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if validation.Field != "provider" {
		t.Errorf("field = %q, want provider", validation.Field)
	}
}
