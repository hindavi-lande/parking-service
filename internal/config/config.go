// Package config loads runtime settings from environment variables.
package config

import (
	"os"
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

	// The Twilio settings are empty unless the provider is configured, which is
	// what selects the log-only notifier for local development and tests.
	TwilioAccountSID string
	TwilioAuthToken  string
	TwilioFromNumber string
}

// Load reads the configuration, falling back to local-friendly defaults so the
// service starts with no environment set up.
func Load() Config {
	return Config{
		Env:             stringEnv("APP_ENV", "local"),
		Addr:            stringEnv("APP_ADDR", ":8080"),
		ReadTimeout:     durationEnv("HTTP_READ_TIMEOUT", 5*time.Second),
		WriteTimeout:    durationEnv("HTTP_WRITE_TIMEOUT", 10*time.Second),
		IdleTimeout:     durationEnv("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout: durationEnv("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),

		TwilioAccountSID: stringEnv("TWILIO_ACCOUNT_SID", ""),
		TwilioAuthToken:  stringEnv("TWILIO_AUTH_TOKEN", ""),
		TwilioFromNumber: stringEnv("TWILIO_FROM_NUMBER", ""),
	}
}

// TwilioConfigured reports whether all three Twilio settings are present. Only
// then is the Twilio notifier used instead of the log-only one.
func (c Config) TwilioConfigured() bool {
	return c.TwilioAccountSID != "" && c.TwilioAuthToken != "" && c.TwilioFromNumber != ""
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
