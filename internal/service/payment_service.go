package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/payment"
	"github.com/techverito/parking-service/internal/store"
)

// PaymentService prices a finished session and tracks the payment through the
// provider. It owns every money rule: the rate comes from the lot, the fee
// rounds up to whole started hours with a one-hour minimum, and the amount is
// an integer in minor units.
type PaymentService struct {
	payments          store.PaymentStore
	lots              store.LotStore
	provider          payment.Provider
	defaultHourlyRate int64
	now               func() time.Time
	// logger records a rejected provider callback, such as a captured event
	// whose amount or currency does not match the stored payment. It never logs
	// a provider credential.
	logger *slog.Logger
}

// NewPaymentService wires the payment rules to their collaborators.
// defaultHourlyRate is the PAYMENT_HOURLY_RATE setting in minor units and
// applies to every lot that carries no hourlyRate of its own.
func NewPaymentService(payments store.PaymentStore, lots store.LotStore, provider payment.Provider, defaultHourlyRate int64, now func() time.Time) *PaymentService {
	if now == nil {
		now = time.Now
	}
	return &PaymentService{
		payments:          payments,
		lots:              lots,
		provider:          provider,
		defaultHourlyRate: defaultHourlyRate,
		now:               now,
		logger:            slog.Default(),
	}
}

// CreateForCheckout returns the payment of a finished session, creating a
// PENDING one the first time. The payment is stored before the provider is
// asked, so a provider failure leaves it PENDING and a retry gets that same
// payment back instead of opening a second one.
func (s *PaymentService) CreateForCheckout(ctx context.Context, session domain.Session) (domain.Payment, error) {
	if s.provider == nil {
		return domain.Payment{}, errors.New("create payment: no payment provider configured")
	}

	existing, err := s.payments.GetBySession(ctx, session.ID)
	switch {
	case err == nil:
		return existing, nil
	case !errors.Is(err, domain.ErrNotFound):
		return domain.Payment{}, fmt.Errorf("get payment for session %q: %w", session.ID, err)
	}

	lot, err := s.lots.Get(ctx, session.LotID)
	if err != nil {
		return domain.Payment{}, fmt.Errorf("get lot %q: %w", session.LotID, err)
	}

	amount, err := FeeAmount(session, s.hourlyRateFor(lot))
	if err != nil {
		return domain.Payment{}, err
	}

	now := s.now().UTC()
	pending := domain.Payment{
		ID:        newID(),
		SessionID: session.ID,
		LotID:     session.LotID,
		Amount:    amount,
		Currency:  domain.CurrencyINR,
		Provider:  s.provider.Name(),
		Status:    domain.PaymentPending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// The payment is stored before the provider is asked, so a provider that
	// times out or answers 5xx leaves a PENDING payment behind and a retry finds
	// it instead of opening a second one.
	if err := s.payments.Create(ctx, pending); err != nil {
		if !errors.Is(err, domain.ErrPaymentExists) {
			return domain.Payment{}, fmt.Errorf("store payment for session %q: %w", session.ID, err)
		}
		existing, err := s.payments.GetBySession(ctx, session.ID)
		if err != nil {
			return domain.Payment{}, fmt.Errorf("get payment for session %q: %w", session.ID, err)
		}
		return existing, nil
	}

	result, err := s.provider.CreatePayment(ctx, payment.CreateRequest{
		PaymentID: pending.ID,
		SessionID: pending.SessionID,
		LotID:     pending.LotID,
		Amount:    pending.Amount,
		Currency:  pending.Currency,
	})
	if err != nil {
		return domain.Payment{}, fmt.Errorf("create payment for session %q: %w", session.ID, err)
	}

	// The client secret and the provider's order details are the parts of the
	// provider's answer the service keeps, so the stored payment carries them
	// until the payment is settled. The order details survive a reload, so a
	// retried checkout answers with the same order instead of opening a second
	// one.
	if result.ClientSecret != "" || result.OrderID != "" || result.KeyID != "" {
		pending.ClientSecret = result.ClientSecret
		pending.ProviderOrderID = result.OrderID
		pending.ProviderKeyID = result.KeyID
		if err := s.payments.Update(ctx, pending); err != nil {
			return domain.Payment{}, fmt.Errorf("store provider details for session %q: %w", session.ID, err)
		}
	}
	return pending, nil
}

// hourlyRateFor resolves the rate a lot is charged at. A lot rate always wins,
// including zero, which means free parking; a lot without one falls back to the
// configured PAYMENT_HOURLY_RATE.
func (s *PaymentService) hourlyRateFor(lot domain.Lot) int64 {
	if lot.HourlyRate != nil {
		return int64(*lot.HourlyRate)
	}
	return s.defaultHourlyRate
}

func (s *PaymentService) Get(ctx context.Context, id string) (domain.Payment, error) {
	stored, err := s.payments.Get(ctx, id)
	if err != nil {
		return domain.Payment{}, fmt.Errorf("get payment %q: %w", id, err)
	}
	return stored, nil
}

// HandleWebhook turns a provider callback into a status change. The path must
// name the configured provider, and the payload must name a payment the
// service knows.
func (s *PaymentService) HandleWebhook(ctx context.Context, providerName string, req payment.WebhookRequest) (domain.Payment, error) {
	if s.provider == nil {
		return domain.Payment{}, errors.New("handle webhook: no payment provider configured")
	}
	if !strings.EqualFold(strings.TrimSpace(providerName), strings.TrimSpace(s.provider.Name())) {
		return domain.Payment{}, &domain.ValidationError{
			Field:   "provider",
			Message: fmt.Sprintf("must be %q", s.provider.Name()),
		}
	}

	event, err := s.provider.ParseWebhook(ctx, req)
	if err != nil {
		return domain.Payment{}, &domain.ValidationError{Field: "body", Message: fmt.Sprintf("is not a valid %s webhook: %v", s.provider.Name(), err)}
	}

	existing, err := s.payments.Get(ctx, event.PaymentID)
	if err != nil {
		return domain.Payment{}, fmt.Errorf("get payment %q: %w", event.PaymentID, err)
	}
	return s.applyStatus(ctx, existing, event.Status, event.FailureMessage)
}

// HandleStripeWebhook applies a Stripe callback to a pending payment. Unlike
// HandleWebhook it is idempotent: a repeated event id, or an event for a payment
// that already reached a terminal status, returns the stored payment and a nil
// error, so Stripe can stop retrying. The route is fixed, so the configured
// provider has to be Stripe for this entry point to apply.
func (s *PaymentService) HandleStripeWebhook(ctx context.Context, req payment.WebhookRequest) (domain.Payment, error) {
	if s.provider == nil {
		return domain.Payment{}, errors.New("handle stripe webhook: no payment provider configured")
	}
	if s.provider.Name() != payment.StripeProviderName {
		return domain.Payment{}, &domain.ValidationError{
			Field:   "provider",
			Message: fmt.Sprintf("must be %q", payment.StripeProviderName),
		}
	}

	event, err := s.provider.ParseWebhook(ctx, req)
	if err != nil {
		return domain.Payment{}, &domain.ValidationError{Field: "body", Message: fmt.Sprintf("is not a valid %s webhook: %v", payment.StripeProviderName, err)}
	}

	existing, err := s.payments.Get(ctx, event.PaymentID)
	if err != nil {
		return domain.Payment{}, fmt.Errorf("get payment %q: %w", event.PaymentID, err)
	}

	// An event that was already handled and an event for a payment that can no
	// longer change are both reported as success: the caller gets the stored
	// payment back and nothing is written.
	if event.EventID != "" {
		processed, err := s.payments.IsEventProcessed(ctx, event.EventID)
		if err != nil {
			return domain.Payment{}, fmt.Errorf("check stripe event %q: %w", event.EventID, err)
		}
		if processed {
			return existing, nil
		}
	}
	if existing.IsTerminal() {
		return existing, nil
	}

	settled, err := s.applyStatus(ctx, existing, event.Status, event.FailureMessage)
	if err != nil {
		return domain.Payment{}, err
	}

	// The event id is marked only once the status change is stored, so a failed
	// write leaves the callback retryable.
	if event.EventID != "" {
		if err := s.payments.MarkEventProcessed(ctx, event.EventID); err != nil {
			return domain.Payment{}, fmt.Errorf("mark stripe event %q: %w", event.EventID, err)
		}
	}
	return settled, nil
}

// HandleRazorpayWebhook applies a Razorpay callback to a pending payment. Like
// HandleStripeWebhook it is idempotent: a repeated event id, or an event for a
// payment that already reached a terminal status, returns the stored payment
// and a nil error, so Razorpay can stop retrying. It differs in two ways: the
// event may name the session instead of the payment, and a captured event has to
// report the amount and currency the payment was created for. A mismatch is
// logged, records the event id as consumed and returns a validation error, so
// the payment stays PENDING and a replay changes nothing.
func (s *PaymentService) HandleRazorpayWebhook(ctx context.Context, req payment.WebhookRequest) (domain.Payment, error) {
	if s.provider == nil {
		return domain.Payment{}, errors.New("handle razorpay webhook: no payment provider configured")
	}
	if s.provider.Name() != payment.RazorpayProviderName {
		return domain.Payment{}, &domain.ValidationError{
			Field:   "provider",
			Message: fmt.Sprintf("must be %q", payment.RazorpayProviderName),
		}
	}

	event, err := s.provider.ParseWebhook(ctx, req)
	if err != nil {
		return domain.Payment{}, &domain.ValidationError{Field: "body", Message: fmt.Sprintf("is not a valid %s webhook: %v", payment.RazorpayProviderName, err)}
	}

	existing, err := s.resolveWebhookPayment(ctx, event)
	if err != nil {
		return domain.Payment{}, err
	}

	// An event that was already handled and an event for a payment that can no
	// longer change are both reported as success: the caller gets the stored
	// payment back and nothing is written.
	if event.EventID != "" {
		processed, err := s.payments.IsEventProcessed(ctx, event.EventID)
		if err != nil {
			return domain.Payment{}, fmt.Errorf("check razorpay event %q: %w", event.EventID, err)
		}
		if processed {
			return existing, nil
		}
	}
	if existing.IsTerminal() {
		return existing, nil
	}

	// A callback that reports a different amount or currency than the payment
	// was created for is not applied. The event id is recorded so a replay is
	// answered with the stored payment, and the rejection names the field while
	// the payment stays PENDING.
	field, expected, reported := "", "", ""
	switch {
	case event.Amount != nil && *event.Amount != existing.Amount:
		field = "amount"
		expected = fmt.Sprintf("%d", existing.Amount)
		reported = fmt.Sprintf("%d", *event.Amount)
	case event.Currency != "" && !strings.EqualFold(strings.TrimSpace(event.Currency), existing.Currency):
		field = "currency"
		expected = existing.Currency
		reported = event.Currency
	}
	if field != "" {
		if event.EventID != "" {
			if err := s.payments.MarkEventProcessed(ctx, event.EventID); err != nil {
				return domain.Payment{}, fmt.Errorf("mark razorpay event %q: %w", event.EventID, err)
			}
		}
		s.logger.Error("reject razorpay webhook: reported value does not match the payment",
			"payment_id", existing.ID,
			"event_id", event.EventID,
			"field", field,
			"expected", expected,
			"reported", reported,
		)
		return domain.Payment{}, &domain.ValidationError{
			Field:   field,
			Message: fmt.Sprintf("does not match the stored payment: expected %s, got %s", expected, reported),
		}
	}

	settled, err := s.applyStatus(ctx, existing, event.Status, event.FailureMessage)
	if err != nil {
		return domain.Payment{}, err
	}

	// The event id is marked only once the status change is stored, so a failed
	// write leaves the callback retryable.
	if event.EventID != "" {
		if err := s.payments.MarkEventProcessed(ctx, event.EventID); err != nil {
			return domain.Payment{}, fmt.Errorf("mark razorpay event %q: %w", event.EventID, err)
		}
	}
	return settled, nil
}

// resolveWebhookPayment finds the stored payment a callback refers to. Razorpay
// names the payment when it has one and otherwise only the session, so the
// session index is the fallback. An event that names neither cannot be applied.
func (s *PaymentService) resolveWebhookPayment(ctx context.Context, event payment.WebhookEvent) (domain.Payment, error) {
	if event.PaymentID != "" {
		existing, err := s.payments.Get(ctx, event.PaymentID)
		if err != nil {
			return domain.Payment{}, fmt.Errorf("get payment %q: %w", event.PaymentID, err)
		}
		return existing, nil
	}
	if event.SessionID != "" {
		existing, err := s.payments.GetBySession(ctx, event.SessionID)
		if err != nil {
			return domain.Payment{}, fmt.Errorf("get payment for session %q: %w", event.SessionID, err)
		}
		return existing, nil
	}
	return domain.Payment{}, &domain.ValidationError{
		Field:   "payment",
		Message: "the event names neither a payment nor a session",
	}
}

// applyStatus is the PENDING → PAID / PENDING → FAILED state machine. A payment
// that already reached a terminal status never changes again, so a repeated or
// out-of-order callback cannot rewrite history. Reaching a terminal status also
// drops the client secret, which the client has no further use for, and a
// failure keeps the provider's reason.
func (s *PaymentService) applyStatus(ctx context.Context, payment domain.Payment, status domain.PaymentStatus, failureMessage string) (domain.Payment, error) {
	if status != domain.PaymentPaid && status != domain.PaymentFailed {
		return domain.Payment{}, &domain.ValidationError{Field: "status", Message: "must be PAID or FAILED"}
	}
	if payment.Status != domain.PaymentPending {
		return domain.Payment{}, fmt.Errorf("payment %q: %w", payment.ID, domain.ErrPaymentNotPending)
	}

	payment.Status = status
	payment.ClientSecret = ""
	payment.FailureMessage = ""
	if status == domain.PaymentFailed {
		payment.FailureMessage = strings.TrimSpace(failureMessage)
	}
	payment.UpdatedAt = s.now().UTC()
	if err := s.payments.Update(ctx, payment); err != nil {
		return domain.Payment{}, fmt.Errorf("update payment %q: %w", payment.ID, err)
	}
	return payment, nil
}

// FeeAmount prices a finished session at hourlyRate minor units per started
// hour with a one-hour minimum. It uses integer arithmetic only.
func FeeAmount(session domain.Session, hourlyRate int64) (int64, error) {
	if session.CheckedOutAt == nil {
		return 0, &domain.ValidationError{Field: "checkedOutAt", Message: "is required to price a session"}
	}
	if hourlyRate < 0 {
		return 0, &domain.ValidationError{Field: "hourlyRate", Message: "must not be negative"}
	}
	return billableHours(session.CheckedInAt, *session.CheckedOutAt) * hourlyRate, nil
}

// billableHours rounds the stay up to whole started hours and never returns
// less than one, so a stay of a few seconds and a zero-length stay are both
// billed one hour.
func billableHours(start, end time.Time) int64 {
	stay := end.Sub(start)
	if stay <= 0 {
		return 1
	}

	hours := int64(stay / time.Hour)
	if stay%time.Hour != 0 {
		hours++
	}
	return hours
}
