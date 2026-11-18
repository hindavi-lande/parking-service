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

	// Stripe settings. StripeSecretKey and StripeWebhookSecret are required when
	// PaymentProvider is stripe, are never logged and never appear in an error
	// message. StripeBaseURL is optional; empty means the Stripe API default.
	StripeSecretKey     string
	StripeWebhookSecret string
	StripeBaseURL       string

	// Twilio settings for the confirmation SMS. Empty means the service uses the
	// log-only notifier, and no default is ever a real credential.
	TwilioAccountSID string
	TwilioAuthToken  string
	TwilioFromNumber string
}

// Load reads the configuration, falling back to local-friendly defaults so the
// service starts with no environment set up. PAYMENT_HOURLY_RATE has no
// default, and a missing, non-numeric or negative value is an error so the
// process cannot start without a usable rate. The two Stripe secrets are
// required only when PAYMENT_PROVIDER selects stripe.
func Load() (Config, error) {
	cfg := Config{
		Env:             stringEnv("APP_ENV", "local"),
		Addr:            stringEnv("APP_ADDR", ":8080"),
		ReadTimeout:     durationEnv("HTTP_READ_TIMEOUT", 5*time.Second),
		WriteTimeout:    durationEnv("HTTP_WRITE_TIMEOUT", 10*time.Second),
		IdleTimeout:     durationEnv("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout: durationEnv("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),

		PaymentProvider: stringEnv("PAYMENT_PROVIDER", "fake"),

		StripeSecretKey:     stringEnv("STRIPE_SECRET_KEY", ""),
		StripeWebhookSecret: stringEnv("STRIPE_WEBHOOK_SECRET", ""),
		StripeBaseURL:       stringEnv("STRIPE_API_BASE_URL", ""),

		TwilioAccountSID: stringEnv("TWILIO_ACCOUNT_SID", ""),
		TwilioAuthToken:  stringEnv("TWILIO_AUTH_TOKEN", ""),
		TwilioFromNumber: stringEnv("TWILIO_FROM_NUMBER", ""),
	}

	hourlyRate, err := requiredInt64Env("PAYMENT_HOURLY_RATE")
	if err != nil {
		return cfg, err
	}
	cfg.PaymentHourlyRate = hourlyRate

	// The Stripe provider cannot run without its two secrets, so the process
	// fails at startup instead of on the first checkout. The error names the
	// variable and never echoes a value.
	if strings.EqualFold(strings.TrimSpace(cfg.PaymentProvider), "stripe") {
		secretKey, err := requiredStringEnv("STRIPE_SECRET_KEY")
		if err != nil {
			return cfg, err
		}
		webhookSecret, err := requiredStringEnv("STRIPE_WEBHOOK_SECRET")
		if err != nil {
			return cfg, err
		}
		cfg.StripeSecretKey = secretKey
		cfg.StripeWebhookSecret = webhookSecret
	}

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

// requiredStringEnv reads a required setting and trims it. The error names the
// variable only: the value may be a credential and is never echoed.
func requiredStringEnv(key string) (string, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return "", fmt.Errorf("%s is required and must not be empty", key)
	}
	return value, nil
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
