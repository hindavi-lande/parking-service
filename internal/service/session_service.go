package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/notify"
	"github.com/techverito/parking-service/internal/store"
)

const (
	minPlateLength = 2
	maxPlateLength = 15

	// smsTimeout bounds the single best-effort confirmation attempt, so a
	// hanging provider cannot keep the check-in response open.
	smsTimeout = 3 * time.Second
)

// checkoutPayments is the narrow slice of the payment service a checkout needs.
// It stays an interface so the session flow can be exercised on its own.
type checkoutPayments interface {
	CreateForCheckout(ctx context.Context, session domain.Session) (domain.Payment, error)
}

// SessionService checks vehicles in and out and enforces lot capacity.
type SessionService struct {
	lots     store.LotStore
	sessions store.SessionStore
	payments checkoutPayments
	now      func() time.Time
	notifier notify.Notifier
	logger   *slog.Logger

	// checkInMu makes the capacity check and the insert one step while the
	// in-memory store is in use. A database transaction replaces it.
	checkInMu sync.Mutex
}

func NewSessionService(lots store.LotStore, sessions store.SessionStore, payments checkoutPayments, now func() time.Time, notifier notify.Notifier, logger *slog.Logger) *SessionService {
	if now == nil {
		now = time.Now
	}
	if notifier == nil {
		notifier = notify.NewLogNotifier(slog.Default())
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionService{lots: lots, sessions: sessions, payments: payments, now: now, notifier: notifier, logger: logger}
}

// CheckInInput is the caller-supplied part of a check-in.
type CheckInInput struct {
	Plate string
	Kind  string
	// Phone is optional. It is used only to send the confirmation and is never
	// stored on the session; when empty no message is sent.
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

	session, err := s.createActiveSession(ctx, lot, plate, kind)
	if err != nil {
		return domain.Session{}, err
	}

	if phone != "" {
		s.sendConfirmation(ctx, phone, lot.Name, session)
	}
	return session, nil
}

// createActiveSession enforces the lot capacity and inserts the session under
// one lock. The lock is released before the confirmation is sent, so a slow
// provider cannot block other check-ins.
func (s *SessionService) createActiveSession(ctx context.Context, lot domain.Lot, plate string, kind domain.SessionKind) (domain.Session, error) {
	s.checkInMu.Lock()
	defer s.checkInMu.Unlock()

	active, err := s.sessions.CountActiveByLot(ctx, lot.ID)
	if err != nil {
		return domain.Session{}, fmt.Errorf("count active sessions in lot %q: %w", lot.ID, err)
	}
	if active >= lot.Capacity {
		return domain.Session{}, fmt.Errorf("lot %q: %w", lot.ID, domain.ErrLotFull)
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

// sendConfirmation makes exactly one best-effort attempt for a session that is
// already stored. The attempt outlives a client disconnect and is capped by
// smsTimeout; a failure is logged and never returned, so it cannot fail the
// check-in.
func (s *SessionService) sendConfirmation(ctx context.Context, phone, lotName string, session domain.Session) {
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), smsTimeout)
	defer cancel()

	msg := notify.Message{
		To:   phone,
		Body: notify.CheckInMessage(lotName, session.Plate, session.CheckedInAt),
	}
	if err := s.notifier.Send(sendCtx, msg); err != nil {
		s.logger.Error("send check-in confirmation",
			"session_id", session.ID,
			"error", err,
		)
	}
}

func (s *SessionService) Get(ctx context.Context, id string) (domain.Session, error) {
	session, err := s.sessions.Get(ctx, id)
	if err != nil {
		return domain.Session{}, fmt.Errorf("get session %q: %w", id, err)
	}
	return session, nil
}

// Checkout ends an active session and returns it together with its payment.
// Checking out twice is a conflict, not a silent success, so a retried request
// can neither extend a stay nor charge again. The session is written before the
// payment is opened, so a pricing or provider failure leaves the vehicle checked
// out with a PENDING payment instead of putting it back in the lot.
func (s *SessionService) Checkout(ctx context.Context, id string) (domain.Session, domain.Payment, error) {
	if s.payments == nil {
		return domain.Session{}, domain.Payment{}, errors.New("checkout: no payment service configured")
	}

	session, err := s.sessions.Get(ctx, id)
	if err != nil {
		return domain.Session{}, domain.Payment{}, fmt.Errorf("get session %q: %w", id, err)
	}
	if session.Status != domain.SessionActive {
		return domain.Session{}, domain.Payment{}, fmt.Errorf("session %q: %w", id, domain.ErrSessionNotActive)
	}

	checkedOutAt := s.now().UTC()
	session.Status = domain.SessionCheckedOut
	session.CheckedOutAt = &checkedOutAt

	// The session is stored first: a provider timeout or 5xx must leave it
	// checked out, and the PENDING payment the service persisted is what a
	// retry finds.
	if err := s.sessions.Update(ctx, session); err != nil {
		return domain.Session{}, domain.Payment{}, fmt.Errorf("update session %q: %w", id, err)
	}

	created, err := s.payments.CreateForCheckout(ctx, session)
	if err != nil {
		return domain.Session{}, domain.Payment{}, err
	}
	return session, created, nil
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

// e164Pattern matches a full E.164 number: a leading plus, a non-zero first
// digit and 8 to 15 digits in total.
var e164Pattern = regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)

// normalizePhone trims the optional phone number. An empty value means the
// caller wants no message; anything else must already be E.164, because
// national formats are not converted.
func normalizePhone(raw string) (string, error) {
	phone := strings.TrimSpace(raw)
	if phone == "" {
		return "", nil
	}
	if !e164Pattern.MatchString(phone) {
		return "", &domain.ValidationError{Field: "phone", Message: "must be a valid E.164 number"}
	}
	return phone, nil
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
