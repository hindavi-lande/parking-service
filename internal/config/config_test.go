package config_test

import (
	"strings"
	"testing"

	"github.com/techverito/parking-service/internal/config"
)

func TestLoadRequiresPaymentHourlyRate(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "unset", value: ""},
		{name: "not a number", value: "costly"},
		{name: "negative", value: "-100"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PAYMENT_HOURLY_RATE", tc.value)

			_, err := config.Load()
			if err == nil {
				t.Fatal("expected a configuration error")
			}
			if !strings.Contains(err.Error(), "PAYMENT_HOURLY_RATE") {
				t.Errorf("error = %v, want it to name PAYMENT_HOURLY_RATE", err)
			}
		})
	}
}

func TestLoadReadsPaymentSettings(t *testing.T) {
	t.Setenv("PAYMENT_HOURLY_RATE", "5000")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PaymentHourlyRate != 5000 {
		t.Errorf("hourly rate = %d, want 5000", cfg.PaymentHourlyRate)
	}
	if cfg.PaymentProvider != "fake" {
		t.Errorf("provider = %q, want fake", cfg.PaymentProvider)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("addr = %q, want the existing default :8080", cfg.Addr)
	}

	t.Setenv("PAYMENT_PROVIDER", "stripe")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PaymentProvider != "stripe" {
		t.Errorf("provider = %q, want stripe", cfg.PaymentProvider)
	}
}
