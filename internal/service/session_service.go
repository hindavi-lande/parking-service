package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/store"
)

const (
	minPlateLength = 2
	maxPlateLength = 15
)

// SessionService checks vehicles in and out and enforces lot capacity.
type SessionService struct {
	lots     store.LotStore
	sessions store.SessionStore
	now      func() time.Time

	// checkInMu makes the capacity check and the insert one step while the
	// in-memory store is in use. A database transaction replaces it.
	checkInMu sync.Mutex
}

func NewSessionService(lots store.LotStore, sessions store.SessionStore, now func() time.Time) *SessionService {
	if now == nil {
		now = time.Now
	}
	return &SessionService{lots: lots, sessions: sessions, now: now}
}

// CheckInInput is the caller-supplied part of a check-in.
type CheckInInput struct {
	Plate string
	Kind  string
}

func (s *SessionService) CheckIn(ctx context.Context, lotID string, input CheckInInput) (domain.Session, error) {
	plate, err := normalizePlate(input.Plate)
	if err != nil {
		return domain.Session{}, err
	}
	kind, err := normalizeKind(input.Kind)
	if err != nil {
		return domain.Session{}, err
	}

	lot, err := s.lots.Get(ctx, lotID)
	if err != nil {
		return domain.Session{}, fmt.Errorf("get lot %q: %w", lotID, err)
	}

	s.checkInMu.Lock()
	defer s.checkInMu.Unlock()

	active, err := s.sessions.CountActiveByLot(ctx, lotID)
	if err != nil {
		return domain.Session{}, fmt.Errorf("count active sessions in lot %q: %w", lotID, err)
	}
	if active >= lot.Capacity {
		return domain.Session{}, fmt.Errorf("lot %q: %w", lotID, domain.ErrLotFull)
	}

	session := domain.Session{
		ID:          newID(),
		LotID:       lot.ID,
		Plate:       plate,
		Kind:        kind,
		Status:      domain.SessionActive,
		CheckedInAt: s.now().UTC(),
	}
	if err := s.sessions.Create(ctx, session); err != nil {
		return domain.Session{}, fmt.Errorf("create session: %w", err)
	}
	return session, nil
}

func (s *SessionService) Get(ctx context.Context, id string) (domain.Session, error) {
	session, err := s.sessions.Get(ctx, id)
	if err != nil {
		return domain.Session{}, fmt.Errorf("get session %q: %w", id, err)
	}
	return session, nil
}

// Checkout ends an active session. Checking out twice is a conflict, not a
// silent success, so a retried request cannot extend a stay.
func (s *SessionService) Checkout(ctx context.Context, id string) (domain.Session, error) {
	session, err := s.sessions.Get(ctx, id)
	if err != nil {
		return domain.Session{}, fmt.Errorf("get session %q: %w", id, err)
	}
	if session.Status != domain.SessionActive {
		return domain.Session{}, fmt.Errorf("session %q: %w", id, domain.ErrSessionNotActive)
	}

	checkedOutAt := s.now().UTC()
	session.Status = domain.SessionCheckedOut
	session.CheckedOutAt = &checkedOutAt

	if err := s.sessions.Update(ctx, session); err != nil {
		return domain.Session{}, fmt.Errorf("update session %q: %w", id, err)
	}
	return session, nil
}

func normalizePlate(raw string) (string, error) {
	plate := strings.ToUpper(strings.Join(strings.Fields(raw), " "))
	if plate == "" {
		return "", &domain.ValidationError{Field: "plate", Message: "is required"}
	}
	if length := utf8.RuneCountInString(plate); length < minPlateLength || length > maxPlateLength {
		return "", &domain.ValidationError{
			Field:   "plate",
			Message: fmt.Sprintf("must be between %d and %d characters", minPlateLength, maxPlateLength),
		}
	}
	for _, r := range plate {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != ' ' {
			return "", &domain.ValidationError{
				Field:   "plate",
				Message: "may contain letters, digits, spaces and hyphens only",
			}
		}
	}
	return plate, nil
}

func normalizeKind(raw string) (domain.SessionKind, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "", string(domain.KindStandard):
		return domain.KindStandard, nil
	case string(domain.KindEV):
		return domain.KindEV, nil
	default:
		return "", &domain.ValidationError{Field: "kind", Message: "must be STANDARD or EV"}
	}
}
