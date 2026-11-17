// Package memory provides in-memory stores for local development, tests and the
// first version of the service. It is safe for concurrent use.
package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/techverito/parking-service/internal/domain"
)

// LotStore keeps lots in a map for the lifetime of the process.
type LotStore struct {
	mu   sync.RWMutex
	lots map[string]domain.Lot
}

func NewLotStore() *LotStore {
	return &LotStore{lots: make(map[string]domain.Lot)}
}

func (s *LotStore) Create(_ context.Context, lot domain.Lot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lots[lot.ID] = lot
	return nil
}

func (s *LotStore) Get(_ context.Context, id string) (domain.Lot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lot, ok := s.lots[id]
	if !ok {
		return domain.Lot{}, domain.ErrNotFound
	}
	return lot, nil
}

func (s *LotStore) List(_ context.Context) ([]domain.Lot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lots := make([]domain.Lot, 0, len(s.lots))
	for _, lot := range s.lots {
		lots = append(lots, lot)
	}
	sort.Slice(lots, func(i, j int) bool {
		if lots[i].CreatedAt.Equal(lots[j].CreatedAt) {
			return lots[i].ID < lots[j].ID
		}
		return lots[i].CreatedAt.Before(lots[j].CreatedAt)
	})
	return lots, nil
}

// SessionStore keeps sessions in a map for the lifetime of the process.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]domain.Session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]domain.Session)}
}

func (s *SessionStore) Create(_ context.Context, session domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = clone(session)
	return nil
}

func (s *SessionStore) Get(_ context.Context, id string) (domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	if !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	return clone(session), nil
}

func (s *SessionStore) Update(_ context.Context, session domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[session.ID]; !ok {
		return domain.ErrNotFound
	}
	s.sessions[session.ID] = clone(session)
	return nil
}

func (s *SessionStore) CountActiveByLot(_ context.Context, lotID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, session := range s.sessions {
		if session.LotID == lotID && session.Status == domain.SessionActive {
			count++
		}
	}
	return count, nil
}

func clone(session domain.Session) domain.Session {
	if session.CheckedOutAt != nil {
		checkedOutAt := *session.CheckedOutAt
		session.CheckedOutAt = &checkedOutAt
	}
	return session
}
