package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/store"
)

// maxLotCapacity keeps obviously wrong input out without inventing a business
// limit that operations would have to change.
const maxLotCapacity = 10_000

// LotService creates and reads parking lots.
type LotService struct {
	lots store.LotStore
	now  func() time.Time
}

func NewLotService(lots store.LotStore, now func() time.Time) *LotService {
	if now == nil {
		now = time.Now
	}
	return &LotService{lots: lots, now: now}
}

// CreateLotInput is the caller-supplied part of a lot.
type CreateLotInput struct {
	Name     string
	Address  string
	Capacity int
	// HourlyRate is optional. A nil value means the configured default rate
	// applies; zero is a valid rate and means free parking.
	HourlyRate *int
}

func (s *LotService) Create(ctx context.Context, input CreateLotInput) (domain.Lot, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Lot{}, &domain.ValidationError{Field: "name", Message: "is required"}
	}
	address := strings.TrimSpace(input.Address)
	if address == "" {
		return domain.Lot{}, &domain.ValidationError{Field: "address", Message: "is required"}
	}
	if input.Capacity < 1 || input.Capacity > maxLotCapacity {
		return domain.Lot{}, &domain.ValidationError{
			Field:   "capacity",
			Message: fmt.Sprintf("must be between 1 and %d", maxLotCapacity),
		}
	}
	if input.HourlyRate != nil && *input.HourlyRate < 0 {
		return domain.Lot{}, &domain.ValidationError{Field: "hourlyRate", Message: "must not be negative"}
	}

	// The rate is copied so a caller cannot change a stored lot through the
	// pointer it passed in.
	var hourlyRate *int
	if input.HourlyRate != nil {
		rate := *input.HourlyRate
		hourlyRate = &rate
	}

	lot := domain.Lot{
		ID:         newID(),
		Name:       name,
		Address:    address,
		Capacity:   input.Capacity,
		HourlyRate: hourlyRate,
		CreatedAt:  s.now().UTC(),
	}
	if err := s.lots.Create(ctx, lot); err != nil {
		return domain.Lot{}, fmt.Errorf("create lot: %w", err)
	}
	return lot, nil
}

func (s *LotService) Get(ctx context.Context, id string) (domain.Lot, error) {
	lot, err := s.lots.Get(ctx, id)
	if err != nil {
		return domain.Lot{}, fmt.Errorf("get lot %q: %w", id, err)
	}
	return lot, nil
}

func (s *LotService) List(ctx context.Context) ([]domain.Lot, error) {
	lots, err := s.lots.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list lots: %w", err)
	}
	if lots == nil {
		lots = []domain.Lot{}
	}
	return lots, nil
}
