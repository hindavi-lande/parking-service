package service

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
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

// e164Pattern is the E.164 shape the driver's number must have: a leading '+',
// then 1 to 15 digits whose first digit is non-zero.
var e164Pattern = regexp.MustCompile(`^\+[1-9][0-9]{0,14}$`)

// SessionService checks vehicles in and out and enforces lot capacity.
type SessionService struct {
	lots     store.LotStore
	sessions store.SessionStore
	now      func() time.Time

	// notifier sends the check-in confirmation. Notifying is best effort: a
	// failure is logged and never returned, so it cannot fail a check-in.
	notifier Notifier
	logger   *slog.Logger

	// checkInMu makes the capacity check and the insert one step while the
	// in-memory store is in use. A database transaction replaces it.
	checkInMu sync.Mutex
}

func NewSessionService(lots store.LotStore, sessions store.SessionStore, notifier Notifier, logger *slog.Logger, now func() time.Time) *SessionService {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	if notifier == nil {
		notifier = logNotifier{logger: logger}
	}
	return &SessionService{
		lots:     lots,
		sessions: sessions,
		now:      now,
		notifier: notifier,
		logger:   logger,
	}
}

// CheckInInput is the caller-supplied part of a check-in.
type CheckInInput struct {
	Plate string
	Kind  string
	// Phone is optional. When present it is an E.164 number the driver may be
	// sent a confirmation for.
	Phone string
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
	phone, err := normalizePhone(input.Phone)
	if err != nil {
		return domain.Session{}, err
	}

	lot, err := s.lots.Get(ctx, lotID)
	if err != nil {
		return domain.Session{}, fmt.Errorf("get lot %q: %w", lotID, err)
	}

	s.checkInMu.Lock()

	active, err := s.sessions.CountActiveByLot(ctx, lotID)
	if err != nil {
		s.checkInMu.Unlock()
		return domain.Session{}, fmt.Errorf("count active sessions in lot %q: %w", lotID, err)
	}
	if active >= lot.Capacity {
		s.checkInMu.Unlock()
		return domain.Session{}, fmt.Errorf("lot %q: %w", lotID, domain.ErrLotFull)
	}

	session := domain.Session{
		ID:          newID(),
		LotID:       lot.ID,
		Plate:       plate,
		Phone:       phone,
		Kind:        kind,
		Status:      domain.SessionActive,
		CheckedInAt: s.now().UTC(),
	}
	if err := s.sessions.Create(ctx, session); err != nil {
		s.checkInMu.Unlock()
		return domain.Session{}, fmt.Errorf("create session: %w", err)
	}

	// The capacity check and the insert are done, so the lock is released
	// before the send: it is a network call and other check-ins must not wait
	// for it.
	s.checkInMu.Unlock()

	if session.Phone != "" {
		notice := domain.CheckInNotice{
			SessionID:   session.ID,
			LotName:     lot.Name,
			Plate:       session.Plate,
			Phone:       session.Phone,
			CheckedInAt: session.CheckedInAt,
		}
		if err := s.notifier.NotifyCheckIn(ctx, notice); err != nil {
			s.logger.Error("send check-in confirmation",
				"session_id", session.ID,
				"error", err,
			)
		}
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

// normalizePhone treats an absent number as "no confirmation requested" and
// otherwise requires E.164 so the SMS provider never sees a malformed number.
// Only a field that is not sent at all is absent: a value that is present but
// blank (for example "  ") is rejected like any other malformed number.
func normalizePhone(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	phone := strings.TrimSpace(raw)
	if !e164Pattern.MatchString(phone) {
		return "", &domain.ValidationError{
			Field:   "phone",
			Message: "must be in E.164 format, for example +14155552671",
		}
	}
	return phone, nil
}

// logNotifier is the fallback used when the service is built without a
// notifier, so a wiring mistake degrades to a log line instead of a panic. It
// mirrors the log-only notifier internal/notify provides; this package cannot
// import that one because the notifiers depend on the Notifier interface here.
type logNotifier struct {
	logger *slog.Logger
}

func (n logNotifier) NotifyCheckIn(_ context.Context, notice domain.CheckInNotice) error {
	n.logger.Info("check-in confirmation not sent, no notifier configured",
		"to", notice.Phone,
		"session_id", notice.SessionID,
		"lot", notice.LotName,
		"plate", notice.Plate,
	)
	return nil
}
