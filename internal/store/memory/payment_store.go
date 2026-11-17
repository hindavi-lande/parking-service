package memory

import (
	"context"
	"sync"

	"github.com/techverito/parking-service/internal/domain"
)

// PaymentStore keeps at most one payment per session in a map for the lifetime
// of the process.
type PaymentStore struct {
	mu        sync.RWMutex
	payments  map[string]domain.Payment
	bySession map[string]string
}

func NewPaymentStore() *PaymentStore {
	return &PaymentStore{
		payments:  make(map[string]domain.Payment),
		bySession: make(map[string]string),
	}
}

// Create stores a copy of the payment and remembers which session it belongs
// to. A session that already has a payment is rejected so a retried checkout
// cannot charge twice.
func (s *PaymentStore) Create(_ context.Context, payment domain.Payment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bySession[payment.SessionID]; ok {
		return domain.ErrPaymentExists
	}
	s.payments[payment.ID] = payment
	s.bySession[payment.SessionID] = payment.ID
	return nil
}

func (s *PaymentStore) Get(_ context.Context, id string) (domain.Payment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	payment, ok := s.payments[id]
	if !ok {
		return domain.Payment{}, domain.ErrNotFound
	}
	return payment, nil
}

func (s *PaymentStore) GetBySession(_ context.Context, sessionID string) (domain.Payment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.bySession[sessionID]
	if !ok {
		return domain.Payment{}, domain.ErrNotFound
	}
	payment, ok := s.payments[id]
	if !ok {
		return domain.Payment{}, domain.ErrNotFound
	}
	return payment, nil
}

// Update replaces a stored payment. It never moves the session index, so the
// one-payment-per-session guarantee survives a status change.
func (s *PaymentStore) Update(_ context.Context, payment domain.Payment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.payments[payment.ID]; !ok {
		return domain.ErrNotFound
	}
	s.payments[payment.ID] = payment
	return nil
}
