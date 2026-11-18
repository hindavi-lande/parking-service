package service_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	name         string
	createCalls  []payment.CreateRequest
	createErr    error
	clientSecret string
	orderID      string
	keyID        string
	event        payment.WebhookEvent
}

func (p *recordingProvider) Name() string {
	if p.name == "" {
		return payment.FakeProviderName
	}
	return p.name
}

func (p *recordingProvider) CreatePayment(_ context.Context, req payment.CreateRequest) (payment.CreateResult, error) {
	p.createCalls = append(p.createCalls, req)
	if p.createErr != nil {
		return payment.CreateResult{}, p.createErr
	}
	return payment.CreateResult{ClientSecret: p.clientSecret, OrderID: p.orderID, KeyID: p.keyID}, nil
}

func (p *recordingProvider) ParseWebhook(_ context.Context, _ payment.WebhookRequest) (payment.WebhookEvent, error) {
	return p.event, nil
}

// newPaymentService wires a payment service with an in-memory store, an empty
// lot store and the fixed clock.
func newPaymentService(t *testing.T, provider payment.Provider, defaultHourlyRate int64) (*service.PaymentService, *memory.PaymentStore, *memory.LotStore) {
	t.Helper()

	clock := func() time.Time { return paymentClock }
	return newPaymentServiceWithClock(t, provider, defaultHourlyRate, clock)
}

// newPaymentServiceWithClock is newPaymentService with a clock the test owns, so
// a test can move time forward and notice a payment that was rewritten.
func newPaymentServiceWithClock(t *testing.T, provider payment.Provider, defaultHourlyRate int64, clock func() time.Time) (*service.PaymentService, *memory.PaymentStore, *memory.LotStore) {
	t.Helper()

	payments := memory.NewPaymentStore()
	lots := memory.NewLotStore()
	return service.NewPaymentService(payments, lots, provider, defaultHourlyRate, clock), payments, lots
}

// stripeProvider is a recording provider that answers like Stripe and reports
// the given client secret.
func stripeProvider(clientSecret string) *recordingProvider {
	return &recordingProvider{name: payment.StripeProviderName, clientSecret: clientSecret}
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

func TestCreateForCheckoutLeavesPaymentPendingWhenProviderFails(t *testing.T) {
	provider := &recordingProvider{createErr: fmt.Errorf("stripe is down: %w", domain.ErrProviderUnavailable)}
	svc, payments, lots := newPaymentService(t, provider, 5000)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_rejected", lot.ID, time.Hour)
	ctx := context.Background()

	_, err := svc.CreateForCheckout(ctx, session)
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}

	stored, err := payments.GetBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get payment by session: %v", err)
	}
	if stored.Status != domain.PaymentPending {
		t.Errorf("status = %q, want %q to survive the provider failure", stored.Status, domain.PaymentPending)
	}
	if stored.ClientSecret != "" {
		t.Errorf("client secret = %q, want none when the provider failed", stored.ClientSecret)
	}

	// The retry finds the stored payment instead of asking the provider again,
	// so a failed checkout cannot open a second payment intent.
	retried, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("retry create payment: %v", err)
	}
	if retried.ID != stored.ID {
		t.Errorf("retried payment id = %q, want %q", retried.ID, stored.ID)
	}
	if len(provider.createCalls) != 1 {
		t.Errorf("provider calls = %d, want exactly one", len(provider.createCalls))
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

func TestCreateForCheckoutStoresTheProvidersClientSecret(t *testing.T) {
	provider := stripeProvider("cs_test_1")
	svc, payments, lots := newPaymentService(t, provider, 5000)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_secret", lot.ID, time.Hour)
	ctx := context.Background()

	created, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if created.ClientSecret != "cs_test_1" {
		t.Errorf("client secret = %q, want cs_test_1", created.ClientSecret)
	}

	stored, err := payments.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get stored payment: %v", err)
	}
	if stored.ClientSecret != "cs_test_1" {
		t.Errorf("stored client secret = %q, want cs_test_1", stored.ClientSecret)
	}
	if stored.Status != domain.PaymentPending {
		t.Errorf("status = %q, want %q", stored.Status, domain.PaymentPending)
	}
}

func TestCreateForCheckoutStoresProviderOrderDetails(t *testing.T) {
	provider := &recordingProvider{
		name:    payment.RazorpayProviderName,
		orderID: "order_1",
		keyID:   "rzp_test_key_id",
	}
	svc, payments, lots := newPaymentService(t, provider, 5000)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_order", lot.ID, time.Hour)
	ctx := context.Background()

	created, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if created.ProviderOrderID != "order_1" {
		t.Errorf("order id = %q, want order_1", created.ProviderOrderID)
	}
	if created.ProviderKeyID != "rzp_test_key_id" {
		t.Errorf("key id = %q, want the configured key id", created.ProviderKeyID)
	}
	if created.ClientSecret != "" {
		t.Errorf("client secret = %q, want none for a provider without one", created.ClientSecret)
	}

	// The order details are stored with the payment, not only returned once, so
	// reading the payment back or retrying the checkout finds them again.
	stored, err := payments.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get stored payment: %v", err)
	}
	if stored.ProviderOrderID != "order_1" || stored.ProviderKeyID != "rzp_test_key_id" {
		t.Errorf("stored payment = %+v, want the provider order details", stored)
	}

	retried, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("retry create payment: %v", err)
	}
	if retried.ID != created.ID {
		t.Errorf("retried payment id = %q, want %q", retried.ID, created.ID)
	}
	if retried.ProviderOrderID != "order_1" || retried.ProviderKeyID != "rzp_test_key_id" {
		t.Errorf("retried payment = %+v, want the stored order details", retried)
	}
	if len(provider.createCalls) != 1 {
		t.Errorf("provider calls = %d, want exactly one", len(provider.createCalls))
	}
}

// recordingRazorpayProvider answers like Razorpay without any network call: it
// opens an order and replays a configured webhook event.
type recordingRazorpayProvider struct {
	createCalls []payment.CreateRequest
	event       payment.WebhookEvent
}

func (p *recordingRazorpayProvider) Name() string { return payment.RazorpayProviderName }

func (p *recordingRazorpayProvider) CreatePayment(_ context.Context, req payment.CreateRequest) (payment.CreateResult, error) {
	p.createCalls = append(p.createCalls, req)
	return payment.CreateResult{OrderID: "order_1", KeyID: "rzp_test_key_id"}, nil
}

func (p *recordingRazorpayProvider) ParseWebhook(_ context.Context, _ payment.WebhookRequest) (payment.WebhookEvent, error) {
	return p.event, nil
}

// amountPtr is the amount a Razorpay event reports, in minor units.
func amountPtr(amount int64) *int64 {
	return &amount
}

func TestHandleRazorpayWebhookSettlesPayments(t *testing.T) {
	cases := []struct {
		name       string
		status     domain.PaymentStatus
		reason     string
		wantReason string
	}{
		{name: "captured marks the payment paid", status: domain.PaymentPaid},
		{
			name:       "failed marks the payment failed with the provider reason",
			status:     domain.PaymentFailed,
			reason:     "Your card was declined.",
			wantReason: "Your card was declined.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &recordingRazorpayProvider{}
			svc, payments, lots := newPaymentService(t, provider, 5000)
			lot := seededLot(t, lots, nil)
			session := finishedSession("ses_razorpay_settle", lot.ID, time.Hour)
			ctx := context.Background()

			created, err := svc.CreateForCheckout(ctx, session)
			if err != nil {
				t.Fatalf("create payment: %v", err)
			}

			provider.event = payment.WebhookEvent{
				PaymentID:      created.ID,
				Status:         tc.status,
				EventID:        "evt_razorpay_settle",
				FailureMessage: tc.reason,
				Amount:         amountPtr(created.Amount),
				Currency:       created.Currency,
			}
			settled, err := svc.HandleRazorpayWebhook(ctx, payment.WebhookRequest{})
			if err != nil {
				t.Fatalf("razorpay webhook: %v", err)
			}
			if settled.Status != tc.status {
				t.Errorf("status = %q, want %q", settled.Status, tc.status)
			}
			if settled.FailureMessage != tc.wantReason {
				t.Errorf("failure message = %q, want %q", settled.FailureMessage, tc.wantReason)
			}

			stored, err := payments.Get(ctx, created.ID)
			if err != nil {
				t.Fatalf("get payment: %v", err)
			}
			if stored.Status != tc.status || stored.FailureMessage != tc.wantReason {
				t.Errorf("stored payment = %+v, want %q with failure message %q", stored, tc.status, tc.wantReason)
			}
		})
	}
}

// TestHandleRazorpayWebhookRejectsAmountMismatch covers a captured event that
// reports a different amount or currency than the payment was created for: the
// event is rejected, logged and recorded as consumed, so the payment stays
// PENDING and a replay changes nothing.
func TestHandleRazorpayWebhookRejectsAmountMismatch(t *testing.T) {
	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	cases := []struct {
		name     string
		amount   *int64
		currency string
		field    string
	}{
		{name: "amount mismatch", amount: amountPtr(1), currency: domain.CurrencyINR, field: "amount"},
		{name: "currency mismatch", amount: amountPtr(5000), currency: "USD", field: "currency"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()

			provider := &recordingRazorpayProvider{}
			svc, payments, lots := newPaymentService(t, provider, 5000)
			lot := seededLot(t, lots, nil)
			session := finishedSession("ses_razorpay_mismatch", lot.ID, time.Hour)
			ctx := context.Background()

			created, err := svc.CreateForCheckout(ctx, session)
			if err != nil {
				t.Fatalf("create payment: %v", err)
			}

			provider.event = payment.WebhookEvent{
				PaymentID: created.ID,
				Status:    domain.PaymentPaid,
				EventID:   "evt_razorpay_mismatch",
				Amount:    tc.amount,
				Currency:  tc.currency,
			}
			_, err = svc.HandleRazorpayWebhook(ctx, payment.WebhookRequest{})

			var validation *domain.ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
			if validation.Field != tc.field {
				t.Errorf("field = %q, want %q", validation.Field, tc.field)
			}

			stored, err := payments.Get(ctx, created.ID)
			if err != nil {
				t.Fatalf("get payment: %v", err)
			}
			if stored.Status != domain.PaymentPending {
				t.Errorf("status = %q, want %q after a rejected event", stored.Status, domain.PaymentPending)
			}

			// The rejection is logged with the payment and the event, and the
			// event id is consumed so a replay is a no-op.
			logged := logs.String()
			if !bytes.Contains(logs.Bytes(), []byte(created.ID)) || !bytes.Contains(logs.Bytes(), []byte("evt_razorpay_mismatch")) {
				t.Errorf("log = %q, want the payment id and the event id", logged)
			}
			processed, err := payments.IsEventProcessed(ctx, "evt_razorpay_mismatch")
			if err != nil {
				t.Fatalf("is event processed: %v", err)
			}
			if !processed {
				t.Error("evt_razorpay_mismatch is not recorded although it was consumed")
			}

			replayed, err := svc.HandleRazorpayWebhook(ctx, payment.WebhookRequest{})
			if err != nil {
				t.Fatalf("replayed webhook: %v", err)
			}
			if replayed.ID != created.ID || replayed.Status != domain.PaymentPending || !replayed.UpdatedAt.Equal(stored.UpdatedAt) {
				t.Errorf("replayed payment = %+v, want the unchanged %+v", replayed, stored)
			}
		})
	}
}

func TestHandleRazorpayWebhookIsIdempotent(t *testing.T) {
	provider := &recordingRazorpayProvider{}
	now := paymentClock
	clock := func() time.Time { return now }
	svc, payments, lots := newPaymentServiceWithClock(t, provider, 5000, clock)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_razorpay_repeat", lot.ID, time.Hour)
	ctx := context.Background()

	created, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}

	provider.event = payment.WebhookEvent{
		PaymentID: created.ID,
		Status:    domain.PaymentPaid,
		EventID:   "evt_razorpay_paid",
		Amount:    amountPtr(created.Amount),
		Currency:  created.Currency,
	}
	first, err := svc.HandleRazorpayWebhook(ctx, payment.WebhookRequest{})
	if err != nil {
		t.Fatalf("razorpay webhook: %v", err)
	}
	now = now.Add(time.Hour)

	// The same event id again: success, and the payment is untouched.
	repeated, err := svc.HandleRazorpayWebhook(ctx, payment.WebhookRequest{})
	if err != nil {
		t.Fatalf("repeated webhook: %v", err)
	}
	if repeated.Status != domain.PaymentPaid || !repeated.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("payment = %+v, want the unchanged %+v", repeated, first)
	}

	// A different event for the now-terminal payment: success, still untouched.
	provider.event = payment.WebhookEvent{
		PaymentID:      created.ID,
		Status:         domain.PaymentFailed,
		EventID:        "evt_razorpay_late",
		FailureMessage: "too late",
		Amount:         amountPtr(created.Amount),
		Currency:       created.Currency,
	}
	terminal, err := svc.HandleRazorpayWebhook(ctx, payment.WebhookRequest{})
	if err != nil {
		t.Fatalf("terminal webhook: %v", err)
	}
	if terminal.Status != domain.PaymentPaid || terminal.FailureMessage != "" || !terminal.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("payment = %+v, want the unchanged %+v", terminal, first)
	}

	stored, err := payments.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if stored.Status != domain.PaymentPaid || stored.FailureMessage != "" || !stored.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("stored payment = %+v, want the paid payment unchanged", stored)
	}

	// The event that changed nothing is not recorded as handled.
	processed, err := payments.IsEventProcessed(ctx, "evt_razorpay_late")
	if err != nil {
		t.Fatalf("is event processed: %v", err)
	}
	if processed {
		t.Error("evt_razorpay_late is marked as processed although it changed nothing")
	}
}

// TestHandleRazorpayWebhookResolvesThePayment covers how the service finds the
// payment a callback refers to: Razorpay names the payment when it has one and
// otherwise only the session.
func TestHandleRazorpayWebhookResolvesThePayment(t *testing.T) {
	provider := &recordingRazorpayProvider{}
	svc, _, lots := newPaymentService(t, provider, 5000)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_razorpay_resolve", lot.ID, time.Hour)
	ctx := context.Background()

	created, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}

	// An event naming neither a payment nor a session cannot be applied.
	provider.event = payment.WebhookEvent{Status: domain.PaymentPaid}
	_, err = svc.HandleRazorpayWebhook(ctx, payment.WebhookRequest{})
	var validation *domain.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}

	provider.event = payment.WebhookEvent{PaymentID: "missing", Status: domain.PaymentPaid}
	if _, err := svc.HandleRazorpayWebhook(ctx, payment.WebhookRequest{}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown payment error = %v, want ErrNotFound", err)
	}

	provider.event = payment.WebhookEvent{SessionID: "missing", Status: domain.PaymentPaid}
	if _, err := svc.HandleRazorpayWebhook(ctx, payment.WebhookRequest{}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown session error = %v, want ErrNotFound", err)
	}

	// An event that names only the session finds its payment through the
	// session index.
	provider.event = payment.WebhookEvent{
		SessionID: session.ID,
		Status:    domain.PaymentPaid,
		EventID:   "evt_razorpay_session_only",
		Amount:    amountPtr(created.Amount),
		Currency:  created.Currency,
	}
	settled, err := svc.HandleRazorpayWebhook(ctx, payment.WebhookRequest{})
	if err != nil {
		t.Fatalf("razorpay webhook: %v", err)
	}
	if settled.ID != created.ID {
		t.Errorf("payment id = %q, want %q", settled.ID, created.ID)
	}
	if settled.Status != domain.PaymentPaid {
		t.Errorf("status = %q, want %q", settled.Status, domain.PaymentPaid)
	}
}

func TestHandleRazorpayWebhookRejectsANonRazorpayProvider(t *testing.T) {
	provider := &recordingProvider{name: payment.FakeProviderName}
	svc, _, _ := newPaymentService(t, provider, 5000)

	_, err := svc.HandleRazorpayWebhook(context.Background(), payment.WebhookRequest{})

	var validation *domain.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if validation.Field != "provider" {
		t.Errorf("field = %q, want provider", validation.Field)
	}
}

func TestHandleStripeWebhookMarksPaidAndClearsTheClientSecret(t *testing.T) {
	provider := stripeProvider("cs_test_1")
	svc, payments, lots := newPaymentService(t, provider, 5000)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_stripe_paid", lot.ID, time.Hour)
	ctx := context.Background()

	created, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}

	provider.event = payment.WebhookEvent{PaymentID: created.ID, Status: domain.PaymentPaid, EventID: "evt_paid"}
	settled, err := svc.HandleStripeWebhook(ctx, payment.WebhookRequest{})
	if err != nil {
		t.Fatalf("stripe webhook: %v", err)
	}
	if settled.Status != domain.PaymentPaid {
		t.Errorf("status = %q, want %q", settled.Status, domain.PaymentPaid)
	}
	if settled.ClientSecret != "" {
		t.Errorf("client secret = %q, want it cleared once the payment is paid", settled.ClientSecret)
	}
	if settled.FailureMessage != "" {
		t.Errorf("failure message = %q, want none for a paid payment", settled.FailureMessage)
	}

	stored, err := payments.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if stored.Status != domain.PaymentPaid || stored.ClientSecret != "" {
		t.Errorf("stored payment = %+v, want PAID without a client secret", stored)
	}

	processed, err := payments.IsEventProcessed(ctx, "evt_paid")
	if err != nil {
		t.Fatalf("is event processed: %v", err)
	}
	if !processed {
		t.Error("evt_paid is not marked as processed")
	}
}

func TestHandleStripeWebhookStoresTheFailureMessage(t *testing.T) {
	provider := stripeProvider("cs_test_1")
	svc, payments, lots := newPaymentService(t, provider, 5000)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_stripe_failed", lot.ID, time.Hour)
	ctx := context.Background()

	created, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}

	provider.event = payment.WebhookEvent{
		PaymentID:      created.ID,
		Status:         domain.PaymentFailed,
		EventID:        "evt_failed",
		FailureMessage: "Your card was declined.",
	}
	settled, err := svc.HandleStripeWebhook(ctx, payment.WebhookRequest{})
	if err != nil {
		t.Fatalf("stripe webhook: %v", err)
	}
	if settled.Status != domain.PaymentFailed {
		t.Errorf("status = %q, want %q", settled.Status, domain.PaymentFailed)
	}
	if settled.FailureMessage != "Your card was declined." {
		t.Errorf("failure message = %q, want the provider's reason", settled.FailureMessage)
	}
	if settled.ClientSecret != "" {
		t.Errorf("client secret = %q, want it cleared once the payment failed", settled.ClientSecret)
	}

	stored, err := payments.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if stored.Status != domain.PaymentFailed || stored.FailureMessage != "Your card was declined." {
		t.Errorf("stored payment = %+v, want FAILED with the failure message", stored)
	}
}

func TestHandleStripeWebhookAnswersSuccessForRepeatedAndTerminalEvents(t *testing.T) {
	provider := stripeProvider("cs_test_1")
	now := paymentClock
	clock := func() time.Time { return now }
	svc, payments, lots := newPaymentServiceWithClock(t, provider, 5000, clock)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_stripe_repeat", lot.ID, time.Hour)
	ctx := context.Background()

	created, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}

	provider.event = payment.WebhookEvent{PaymentID: created.ID, Status: domain.PaymentPaid, EventID: "evt_1"}
	first, err := svc.HandleStripeWebhook(ctx, payment.WebhookRequest{})
	if err != nil {
		t.Fatalf("stripe webhook: %v", err)
	}
	now = now.Add(time.Hour)

	// The same event id again: success, and the payment is untouched.
	repeated, err := svc.HandleStripeWebhook(ctx, payment.WebhookRequest{})
	if err != nil {
		t.Fatalf("repeated webhook: %v", err)
	}
	if repeated.Status != domain.PaymentPaid || !repeated.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("payment = %+v, want the unchanged %+v", repeated, first)
	}

	// A different event for the now-terminal payment: success, still untouched.
	provider.event = payment.WebhookEvent{
		PaymentID:      created.ID,
		Status:         domain.PaymentFailed,
		EventID:        "evt_2",
		FailureMessage: "too late",
	}
	terminal, err := svc.HandleStripeWebhook(ctx, payment.WebhookRequest{})
	if err != nil {
		t.Fatalf("terminal webhook: %v", err)
	}
	if terminal.Status != domain.PaymentPaid || terminal.FailureMessage != "" || !terminal.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("payment = %+v, want the unchanged %+v", terminal, first)
	}

	stored, err := payments.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if stored.Status != domain.PaymentPaid || stored.FailureMessage != "" || !stored.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("stored payment = %+v, want the paid payment unchanged", stored)
	}

	// The event that changed nothing is not recorded as handled.
	processed, err := payments.IsEventProcessed(ctx, "evt_2")
	if err != nil {
		t.Fatalf("is event processed: %v", err)
	}
	if processed {
		t.Error("evt_2 is marked as processed although it changed nothing")
	}
}

func TestHandleStripeWebhookSkipsAnEventIdThatWasAlreadyHandled(t *testing.T) {
	provider := stripeProvider("cs_test_1")
	svc, payments, lots := newPaymentService(t, provider, 5000)
	lot := seededLot(t, lots, nil)
	session := finishedSession("ses_stripe_seen", lot.ID, time.Hour)
	ctx := context.Background()

	created, err := svc.CreateForCheckout(ctx, session)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if err := payments.MarkEventProcessed(ctx, "evt_seen"); err != nil {
		t.Fatalf("mark event processed: %v", err)
	}

	provider.event = payment.WebhookEvent{PaymentID: created.ID, Status: domain.PaymentPaid, EventID: "evt_seen"}
	settled, err := svc.HandleStripeWebhook(ctx, payment.WebhookRequest{})
	if err != nil {
		t.Fatalf("stripe webhook: %v", err)
	}
	if settled.Status != domain.PaymentPending {
		t.Errorf("status = %q, want %q for an event that was already handled", settled.Status, domain.PaymentPending)
	}
}

func TestHandleStripeWebhookRejectsANonStripeProvider(t *testing.T) {
	provider := &recordingProvider{name: payment.FakeProviderName}
	svc, _, _ := newPaymentService(t, provider, 5000)

	_, err := svc.HandleStripeWebhook(context.Background(), payment.WebhookRequest{})

	var validation *domain.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if validation.Field != "provider" {
		t.Errorf("field = %q, want provider", validation.Field)
	}
}
