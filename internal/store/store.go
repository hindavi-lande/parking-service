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
