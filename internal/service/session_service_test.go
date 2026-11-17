package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/service"
	"github.com/techverito/parking-service/internal/store/memory"
)

var testClockTime = time.Date(2026, time.November, 17, 9, 0, 0, 0, time.UTC)

func testClock() time.Time { return testClockTime }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingNotifier stands in for the SMS provider and remembers what it was
// asked to send together with any failure or blocking it was told to simulate.
type recordingNotifier struct {
	mu       sync.Mutex
	calls    int
	notices  []domain.CheckInNotice
	err      error
	entered  chan struct{}
	release  chan struct{}
	enterOne sync.Once
}

func (n *recordingNotifier) NotifyCheckIn(_ context.Context, notice domain.CheckInNotice) error {
	n.mu.Lock()
	n.calls++
	n.notices = append(n.notices, notice)
	entered := n.entered
	release := n.release
	n.mu.Unlock()

	if entered != nil {
		n.enterOne.Do(func() { close(entered) })
	}
	if release != nil {
		<-release
	}
	return n.err
}

func (n *recordingNotifier) callCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls
}

func (n *recordingNotifier) lastNotice() (domain.CheckInNotice, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.notices) == 0 {
		return domain.CheckInNotice{}, false
	}
	return n.notices[len(n.notices)-1], true
}

func newServices(t *testing.T) (*service.LotService, *service.SessionService, domain.Lot) {
	t.Helper()

	return newServicesWithNotifier(t, &recordingNotifier{}, discardLogger())
}

func newServicesWithNotifier(
	t *testing.T,
	notifier service.Notifier,
	logger *slog.Logger,
) (*service.LotService, *service.SessionService, domain.Lot) {
	t.Helper()

	lots := memory.NewLotStore()
	sessions := memory.NewSessionStore()
	lotService := service.NewLotService(lots, testClock)
	sessionService := service.NewSessionService(lots, sessions, notifier, logger, testClock)

	lot, err := lotService.Create(context.Background(), service.CreateLotInput{
		Name:     "Central Plaza",
		Address:  "MG Road",
		Capacity: 1,
	})
	if err != nil {
		t.Fatalf("create lot: %v", err)
	}
	return lotService, sessionService, lot
}

func TestCheckInNormalisesPlateAndDefaultsKind(t *testing.T) {
	_, sessions, lot := newServices(t)

	session, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{
		Plate: "  ka  01 ab 1234 ",
	})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}
	if session.Plate != "KA 01 AB 1234" {
		t.Errorf("plate = %q, want %q", session.Plate, "KA 01 AB 1234")
	}
	if session.Kind != domain.KindStandard {
		t.Errorf("kind = %q, want %q", session.Kind, domain.KindStandard)
	}
	if session.Status != domain.SessionActive {
		t.Errorf("status = %q, want %q", session.Status, domain.SessionActive)
	}
	if !session.CheckedInAt.Equal(time.Date(2026, time.November, 17, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("checkedInAt = %v, want the injected clock time", session.CheckedInAt)
	}
}

func TestCheckInRejectsInvalidPlate(t *testing.T) {
	_, sessions, lot := newServices(t)

	_, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{Plate: "!"})
	if err == nil {
		t.Fatal("expected a validation error")
	}

	var validation *domain.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if validation.Field != "plate" {
		t.Errorf("field = %q, want plate", validation.Field)
	}
}

func TestCheckInRejectsUnknownLot(t *testing.T) {
	_, sessions, _ := newServices(t)

	_, err := sessions.CheckIn(context.Background(), "missing", service.CheckInInput{Plate: "KA 01 AB 1234"})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestCheckInStopsWhenLotIsFull(t *testing.T) {
	_, sessions, lot := newServices(t)
	ctx := context.Background()

	if _, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{Plate: "KA 01 AB 1234"}); err != nil {
		t.Fatalf("first check in: %v", err)
	}

	_, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{Plate: "KA 02 CD 5678"})
	if !errors.Is(err, domain.ErrLotFull) {
		t.Fatalf("error = %v, want ErrLotFull", err)
	}
}

func TestCheckoutTwiceIsAConflict(t *testing.T) {
	_, sessions, lot := newServices(t)
	ctx := context.Background()

	session, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{Plate: "KA 01 AB 1234"})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}

	checkedOut, err := sessions.Checkout(ctx, session.ID)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if checkedOut.Status != domain.SessionCheckedOut {
		t.Errorf("status = %q, want %q", checkedOut.Status, domain.SessionCheckedOut)
	}
	if checkedOut.CheckedOutAt == nil {
		t.Fatal("checkedOutAt is nil")
	}

	if _, err := sessions.Checkout(ctx, session.ID); !errors.Is(err, domain.ErrSessionNotActive) {
		t.Fatalf("second checkout error = %v, want ErrSessionNotActive", err)
	}
}

func TestCreateLotValidatesInput(t *testing.T) {
	lotService := service.NewLotService(memory.NewLotStore(), nil)

	cases := []struct {
		name  string
		input service.CreateLotInput
		field string
	}{
		{name: "name required", input: service.CreateLotInput{Address: "MG Road", Capacity: 10}, field: "name"},
		{name: "address required", input: service.CreateLotInput{Name: "Central", Capacity: 10}, field: "address"},
		{name: "capacity required", input: service.CreateLotInput{Name: "Central", Address: "MG Road"}, field: "capacity"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := lotService.Create(context.Background(), tc.input)

			var validation *domain.ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
			if validation.Field != tc.field {
				t.Errorf("field = %q, want %q", validation.Field, tc.field)
			}
		})
	}
}

func TestCheckInStoresValidPhone(t *testing.T) {
	_, sessions, lot := newServices(t)
	ctx := context.Background()

	session, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{
		Plate: "KA 01 AB 1234",
		Phone: "+14155552671",
	})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}
	if session.Phone != "+14155552671" {
		t.Errorf("phone = %q, want %q", session.Phone, "+14155552671")
	}

	stored, err := sessions.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.Phone != "+14155552671" {
		t.Errorf("stored phone = %q, want %q", stored.Phone, "+14155552671")
	}
}

func TestCheckInRejectsInvalidPhone(t *testing.T) {
	cases := []struct {
		name  string
		phone string
	}{
		{name: "missing plus", phone: "4155552671"},
		{name: "zero country code", phone: "+01155552671"},
		{name: "non digit", phone: "+1415555267a"},
		{name: "too many digits", phone: "+1415555267112345"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, sessions, lot := newServices(t)
			ctx := context.Background()

			_, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{
				Plate: "KA 01 AB 1234",
				Phone: tc.phone,
			})

			var validation *domain.ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
			if validation.Field != "phone" {
				t.Errorf("field = %q, want phone", validation.Field)
			}

			// The lot holds one vehicle, so a stored session would make this
			// valid check-in fail with ErrLotFull.
			if _, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{Plate: "KA 02 CD 5678"}); err != nil {
				t.Fatalf("check in after a rejected phone: %v", err)
			}
		})
	}
}

func TestCheckInNotifiesWhenPhonePresent(t *testing.T) {
	notifier := &recordingNotifier{}
	_, sessions, lot := newServicesWithNotifier(t, notifier, discardLogger())

	session, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{
		Plate: "KA 01 AB 1234",
		Phone: "+14155552671",
	})
	if err != nil {
		t.Fatalf("check in: %v", err)
	}

	if got := notifier.callCount(); got != 1 {
		t.Fatalf("notify calls = %d, want exactly one", got)
	}

	notice, ok := notifier.lastNotice()
	if !ok {
		t.Fatal("no notice was recorded")
	}
	if notice.SessionID != session.ID {
		t.Errorf("session id = %q, want %q", notice.SessionID, session.ID)
	}
	if notice.LotName != lot.Name {
		t.Errorf("lot name = %q, want %q", notice.LotName, lot.Name)
	}
	if notice.Plate != "KA 01 AB 1234" {
		t.Errorf("plate = %q, want %q", notice.Plate, "KA 01 AB 1234")
	}
	if notice.Phone != "+14155552671" {
		t.Errorf("phone = %q, want %q", notice.Phone, "+14155552671")
	}
	if !notice.CheckedInAt.Equal(testClockTime) {
		t.Errorf("checkedInAt = %v, want the injected clock time", notice.CheckedInAt)
	}
}

func TestCheckInSkipsNotificationWithoutPhone(t *testing.T) {
	notifier := &recordingNotifier{}
	_, sessions, lot := newServicesWithNotifier(t, notifier, discardLogger())

	if _, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{Plate: "KA 01 AB 1234"}); err != nil {
		t.Fatalf("check in: %v", err)
	}

	if got := notifier.callCount(); got != 0 {
		t.Errorf("notify calls = %d, want none without a phone", got)
	}
}

func TestCheckInSurvivesNotifierFailure(t *testing.T) {
	providerErr := errors.New("twilio rejected the message")
	notifier := &recordingNotifier{err: providerErr}
	var buffer bytes.Buffer
	_, sessions, lot := newServicesWithNotifier(t, notifier, slog.New(slog.NewTextHandler(&buffer, nil)))

	session, err := sessions.CheckIn(context.Background(), lot.ID, service.CheckInInput{
		Plate: "KA 01 AB 1234",
		Phone: "+14155552671",
	})
	if err != nil {
		t.Fatalf("check in returned an error for a failed notification: %v", err)
	}

	stored, err := sessions.Get(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.ID != session.ID {
		t.Errorf("stored session id = %q, want %q", stored.ID, session.ID)
	}

	record := buffer.String()
	if !strings.Contains(record, "level=ERROR") {
		t.Errorf("log record = %q, want an error level record", record)
	}
	if !strings.Contains(record, session.ID) {
		t.Errorf("log record = %q, want it to contain the session id %q", record, session.ID)
	}
	if !strings.Contains(record, providerErr.Error()) {
		t.Errorf("log record = %q, want it to contain the failure %q", record, providerErr)
	}
}

func TestCheckInDoesNotHoldCapacityLockWhileNotifying(t *testing.T) {
	notifier := &recordingNotifier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	lots, sessions, lot := newServicesWithNotifier(t, notifier, discardLogger())
	ctx := context.Background()

	first := make(chan error, 1)
	go func() {
		_, err := sessions.CheckIn(ctx, lot.ID, service.CheckInInput{
			Plate: "KA 01 AB 1234",
			Phone: "+14155552671",
		})
		first <- err
	}()

	select {
	case <-notifier.entered:
	case err := <-first:
		t.Fatalf("check-in finished before notifying: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the notifier was never called")
	}

	otherLot, err := lots.Create(ctx, service.CreateLotInput{
		Name:     "Second Plaza",
		Address:  "MG Road",
		Capacity: 1,
	})
	if err != nil {
		close(notifier.release)
		t.Fatalf("create second lot: %v", err)
	}

	second := make(chan error, 1)
	go func() {
		_, err := sessions.CheckIn(ctx, otherLot.ID, service.CheckInInput{Plate: "KA 02 CD 5678"})
		second <- err
	}()

	select {
	case err := <-second:
		if err != nil {
			close(notifier.release)
			t.Fatalf("check in while the notification is in flight: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(notifier.release)
		t.Fatal("check-in in another lot waited for the notification to finish")
	}

	close(notifier.release)
	if err := <-first; err != nil {
		t.Fatalf("first check in: %v", err)
	}
}
