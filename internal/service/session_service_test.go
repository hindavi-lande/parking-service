package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/service"
	"github.com/techverito/parking-service/internal/store/memory"
)

func newServices(t *testing.T) (*service.LotService, *service.SessionService, domain.Lot) {
	t.Helper()

	fixed := time.Date(2026, time.November, 17, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return fixed }

	lots := memory.NewLotStore()
	sessions := memory.NewSessionStore()
	lotService := service.NewLotService(lots, clock)
	sessionService := service.NewSessionService(lots, sessions, clock)

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
