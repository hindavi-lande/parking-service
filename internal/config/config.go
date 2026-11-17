// Package config loads runtime settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting the service reads from the environment.
type Config struct {
	Env             string
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration

	// Payment settings. The provider defaults to the fake implementation;
	// PaymentHourlyRate is required and is expressed in minor units.
	PaymentProvider   string
	PaymentHourlyRate int64

	// Twilio settings for the confirmation SMS. Empty means the service uses the
	// log-only notifier, and no default is ever a real credential.
	TwilioAccountSID string
	TwilioAuthToken  string
	TwilioFromNumber string
}

// Load reads the configuration, falling back to local-friendly defaults so the
// service starts with no environment set up. PAYMENT_HOURLY_RATE is the one
// exception: it has no default, and a missing, non-numeric or negative value is
// an error so the process cannot start without a usable rate.
func Load() (Config, error) {
	cfg := Config{
		Env:             stringEnv("APP_ENV", "local"),
		Addr:            stringEnv("APP_ADDR", ":8080"),
		ReadTimeout:     durationEnv("HTTP_READ_TIMEOUT", 5*time.Second),
		WriteTimeout:    durationEnv("HTTP_WRITE_TIMEOUT", 10*time.Second),
		IdleTimeout:     durationEnv("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout: durationEnv("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),

		PaymentProvider: stringEnv("PAYMENT_PROVIDER", "fake"),

		TwilioAccountSID: stringEnv("TWILIO_ACCOUNT_SID", ""),
		TwilioAuthToken:  stringEnv("TWILIO_AUTH_TOKEN", ""),
		TwilioFromNumber: stringEnv("TWILIO_FROM_NUMBER", ""),
	}

	hourlyRate, err := requiredInt64Env("PAYMENT_HOURLY_RATE")
	if err != nil {
		return cfg, err
	}
	cfg.PaymentHourlyRate = hourlyRate

	return cfg, nil
}

func stringEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// requiredInt64Env reads a required non-negative integer setting. The error
// names the variable and says why the value was rejected.
func requiredInt64Env(key string) (int64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, fmt.Errorf("%s is required: set the hourly rate in minor units", key)
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer number of minor units: %w", key, err)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %d", key, parsed)
	}
	return parsed, nil
}
