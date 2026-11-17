package service

import (
	"context"
	"errors"
	"fmt"
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
	}
}

// CreateForCheckout returns the payment of a finished session, creating a
// PENDING one the first time. A session that already has a payment gets that
// payment back, so a retried checkout can never charge twice.
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

	// The provider is asked before anything is stored, so a rejected payment
	// leaves no row behind and the checkout can be retried.
	if err := s.provider.CreatePayment(ctx, payment.CreateRequest{
		PaymentID: pending.ID,
		SessionID: pending.SessionID,
		LotID:     pending.LotID,
		Amount:    pending.Amount,
		Currency:  pending.Currency,
	}); err != nil {
		return domain.Payment{}, fmt.Errorf("create payment for session %q: %w", session.ID, err)
	}

	if err := s.payments.Create(ctx, pending); err != nil {
		return domain.Payment{}, fmt.Errorf("store payment for session %q: %w", session.ID, err)
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
	return s.applyStatus(ctx, existing, event.Status)
}

// applyStatus is the PENDING → PAID / PENDING → FAILED state machine. A payment
// that already reached a terminal status never changes again, so a repeated or
// out-of-order callback cannot rewrite history.
func (s *PaymentService) applyStatus(ctx context.Context, payment domain.Payment, status domain.PaymentStatus) (domain.Payment, error) {
	if status != domain.PaymentPaid && status != domain.PaymentFailed {
		return domain.Payment{}, &domain.ValidationError{Field: "status", Message: "must be PAID or FAILED"}
	}
	if payment.Status != domain.PaymentPending {
		return domain.Payment{}, fmt.Errorf("payment %q: %w", payment.ID, domain.ErrPaymentNotPending)
	}

	payment.Status = status
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
