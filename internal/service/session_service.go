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
	"github.com/techverito/parking-service/internal/notify"
	"github.com/techverito/parking-service/internal/store"
)

const (
	minPlateLength = 2
	maxPlateLength = 15

	// maxSMSLength is the number of characters one GSM-7 SMS segment carries.
	// The check-in confirmation has to fit in a single segment.
	maxSMSLength = 160
)

// smsEllipsis ends a shortened lot name. It is three ASCII dots rather than
// U+2026, because the Unicode ellipsis would force UCS-2 encoding and cut the
// message down to 70 characters per segment.
const smsEllipsis = "..."

// gsm7Basic is the GSM 03.38 default alphabet without the characters that need
// an escape sequence. A message built only from these characters fits 160 per
// segment.
const gsm7Basic = "@£$¥èéùìòÇØøÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

// checkInPhonePattern matches an E.164 number: a leading + followed by at most
// 15 digits, the first of which is not zero.
var checkInPhonePattern = regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)

// SessionService checks vehicles in and out and enforces lot capacity.
type SessionService struct {
	lots     store.LotStore
	sessions store.SessionStore
	now      func() time.Time
	notifier notify.Notifier
	logger   *slog.Logger

	// checkInMu makes the capacity check and the insert one step while the
	// in-memory store is in use. A database transaction replaces it.
	checkInMu sync.Mutex
}

// NewSessionService builds the service. A nil clock falls back to time.Now, a
// nil notifier to the log-only one and a nil logger to slog.Default().
func NewSessionService(lots store.LotStore, sessions store.SessionStore, now func() time.Time, notifier notify.Notifier, logger *slog.Logger) *SessionService {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	if notifier == nil {
		notifier = notify.NewLogNotifier(logger)
	}
	return &SessionService{lots: lots, sessions: sessions, now: now, notifier: notifier, logger: logger}
}

// CheckInInput is the caller-supplied part of a check-in.
type CheckInInput struct {
	Plate string
	Kind  string
	// Phone is optional. Empty means no SMS is sent; when set it must be an
	// E.164 number such as +14155552671.
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

	session, err := s.createSession(ctx, lot, plate, phone, kind)
	if err != nil {
		return domain.Session{}, err
	}

	s.sendCheckInConfirmation(ctx, lot, session)
	return session, nil
}

// createSession holds the capacity check and the insert together. The mutex is
// released before any confirmation goes out, so a slow provider cannot block
// other check-ins.
func (s *SessionService) createSession(ctx context.Context, lot domain.Lot, plate, phone string, kind domain.SessionKind) (domain.Session, error) {
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
		Phone:       phone,
		Kind:        kind,
		Status:      domain.SessionActive,
		CheckedInAt: s.now().UTC(),
	}
	if err := s.sessions.Create(ctx, session); err != nil {
		return domain.Session{}, fmt.Errorf("create session: %w", err)
	}
	return session, nil
}

// sendCheckInConfirmation makes one best-effort attempt to text the driver. A
// provider failure is logged with the session id and never fails the check-in
// or removes the stored session.
func (s *SessionService) sendCheckInConfirmation(ctx context.Context, lot domain.Lot, session domain.Session) {
	if session.Phone == "" {
		return
	}

	message := notify.Message{
		To:   session.Phone,
		Body: composeCheckInMessage(lot.Name, session.Plate, session.CheckedInAt),
	}
	if err := s.notifier.Send(ctx, message); err != nil {
		s.logger.Error("send check-in confirmation",
			"error", err,
			"session_id", session.ID,
			"lot_id", session.LotID,
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

// composeCheckInMessage names the lot, the plate and the start time in RFC 3339
// UTC. It always fits one segment by shortening the lot name, the only part of
// the template whose length is unbounded.
func composeCheckInMessage(lotName, plate string, at time.Time) string {
	start := at.UTC().Format(time.RFC3339)
	name := gsm7Name(lotName)

	message := formatCheckInMessage(name, plate, start)
	if utf8.RuneCountInString(message) <= maxSMSLength {
		return message
	}

	fixed := utf8.RuneCountInString(formatCheckInMessage("", plate, start))
	budget := maxSMSLength - fixed - utf8.RuneCountInString(smsEllipsis)
	if budget < 0 {
		budget = 0
	}
	runes := []rune(name)
	if budget > len(runes) {
		budget = len(runes)
	}
	return formatCheckInMessage(string(runes[:budget])+smsEllipsis, plate, start)
}

func formatCheckInMessage(lotName, plate, start string) string {
	return fmt.Sprintf("Parking confirmed. Lot: %s. Plate: %s. Start: %s.", lotName, plate, start)
}

// gsm7Name drops the characters a single GSM-7 segment cannot carry, so a lot
// name in another script cannot silently shrink the segment to 70 characters.
func gsm7Name(name string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(gsm7Basic, r) {
			return r
		}
		return -1
	}, name)
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

// normalizePhone accepts an empty phone, which means no SMS is sent, or an
// E.164 number such as +14155552671.
func normalizePhone(raw string) (string, error) {
	phone := strings.TrimSpace(raw)
	if phone == "" {
		return "", nil
	}
	if !checkInPhonePattern.MatchString(phone) {
		return "", &domain.ValidationError{
			Field:   "phone",
			Message: "must be an E.164 phone number, e.g. +14155552671",
		}
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
