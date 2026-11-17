// Package store defines the persistence contracts. Handlers and services only
// depend on these interfaces, so the in-memory implementation can be replaced
// by Postgres without touching business code.
package store

import (
	"context"

	"github.com/techverito/parking-service/internal/domain"
)

// LotStore stores parking lots.
type LotStore interface {
	Create(ctx context.Context, lot domain.Lot) error
	Get(ctx context.Context, id string) (domain.Lot, error)
	List(ctx context.Context) ([]domain.Lot, error)
}

// SessionStore stores parking sessions.
type SessionStore interface {
	Create(ctx context.Context, session domain.Session) error
	Get(ctx context.Context, id string) (domain.Session, error)
	Update(ctx context.Context, session domain.Session) error
	CountActiveByLot(ctx context.Context, lotID string) (int, error)
}

// PaymentStore stores the payment of a session. A session has at most one
// payment, so a repeated checkout cannot add a second charge.
type PaymentStore interface {
	// Create returns domain.ErrPaymentExists when the session already has a
	// payment.
	Create(ctx context.Context, payment domain.Payment) error
	Get(ctx context.Context, id string) (domain.Payment, error)
	GetBySession(ctx context.Context, sessionID string) (domain.Payment, error)
	Update(ctx context.Context, payment domain.Payment) error
}
