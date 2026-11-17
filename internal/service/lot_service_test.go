package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/service"
	"github.com/techverito/parking-service/internal/store/memory"
)

func intPtr(value int) *int {
	return &value
}

func TestCreateLotRejectsNegativeHourlyRate(t *testing.T) {
	lots := memory.NewLotStore()
	lotService := service.NewLotService(lots, nil)

	_, err := lotService.Create(context.Background(), service.CreateLotInput{
		Name:       "Central Plaza",
		Address:    "MG Road",
		Capacity:   2,
		HourlyRate: intPtr(-1),
	})

	var validation *domain.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if validation.Field != "hourlyRate" {
		t.Errorf("field = %q, want hourlyRate", validation.Field)
	}

	stored, err := lots.List(context.Background())
	if err != nil {
		t.Fatalf("list lots: %v", err)
	}
	if len(stored) != 0 {
		t.Errorf("lots = %+v, want none stored", stored)
	}
}

func TestCreateLotKeepsHourlyRateIncludingZero(t *testing.T) {
	cases := []struct {
		name string
		rate *int
		want *int
	}{
		{name: "unset stays nil", rate: nil, want: nil},
		{name: "zero is free parking", rate: intPtr(0), want: intPtr(0)},
		{name: "explicit rate", rate: intPtr(5000), want: intPtr(5000)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lotService := service.NewLotService(memory.NewLotStore(), nil)

			lot, err := lotService.Create(context.Background(), service.CreateLotInput{
				Name:       "Central Plaza",
				Address:    "MG Road",
				Capacity:   2,
				HourlyRate: tc.rate,
			})
			if err != nil {
				t.Fatalf("create lot: %v", err)
			}

			switch {
			case tc.want == nil && lot.HourlyRate != nil:
				t.Errorf("hourlyRate = %d, want nil", *lot.HourlyRate)
			case tc.want != nil && lot.HourlyRate == nil:
				t.Errorf("hourlyRate = nil, want %d", *tc.want)
			case tc.want != nil && *lot.HourlyRate != *tc.want:
				t.Errorf("hourlyRate = %d, want %d", *lot.HourlyRate, *tc.want)
			}
		})
	}
}
