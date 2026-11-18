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
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_config")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_config")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PaymentProvider != "stripe" {
		t.Errorf("provider = %q, want stripe", cfg.PaymentProvider)
	}
}

func TestLoadRequiresStripeSecretsWhenStripeIsSelected(t *testing.T) {
	cases := []struct {
		name        string
		secretKey   string
		webhookKey  string
		wantMissing string
	}{
		{name: "missing secret key", secretKey: "", webhookKey: "whsec_config", wantMissing: "STRIPE_SECRET_KEY"},
		{name: "missing webhook secret", secretKey: "sk_test_config", webhookKey: "", wantMissing: "STRIPE_WEBHOOK_SECRET"},
		{name: "missing both", secretKey: "", webhookKey: "", wantMissing: "STRIPE_SECRET_KEY"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PAYMENT_HOURLY_RATE", "5000")
			t.Setenv("PAYMENT_PROVIDER", "  STRIPE ")
			t.Setenv("STRIPE_SECRET_KEY", tc.secretKey)
			t.Setenv("STRIPE_WEBHOOK_SECRET", tc.webhookKey)

			_, err := config.Load()
			if err == nil {
				t.Fatal("expected a configuration error")
			}
			if !strings.Contains(err.Error(), tc.wantMissing) {
				t.Errorf("error = %v, want it to name %s", err, tc.wantMissing)
			}
		})
	}
}

func TestLoadNeverEchoesStripeSecrets(t *testing.T) {
	const secretKey = "sk_test_never_echo_this"
	const webhookSecret = "whsec_never_echo_this"

	t.Setenv("PAYMENT_HOURLY_RATE", "5000")
	t.Setenv("PAYMENT_PROVIDER", "stripe")
	t.Setenv("STRIPE_SECRET_KEY", secretKey)
	t.Setenv("STRIPE_WEBHOOK_SECRET", webhookSecret)

	if _, err := config.Load(); err != nil {
		t.Fatalf("load with both secrets: %v", err)
	}

	// A failing load names the missing variable and neither value.
	t.Setenv("STRIPE_WEBHOOK_SECRET", "")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected a configuration error for the missing webhook secret")
	}
	if strings.Contains(err.Error(), secretKey) || strings.Contains(err.Error(), webhookSecret) {
		t.Errorf("error = %v, want neither secret in the message", err)
	}
}

func TestLoadReadsStripeSettings(t *testing.T) {
	t.Setenv("PAYMENT_HOURLY_RATE", "5000")
	t.Setenv("PAYMENT_PROVIDER", "stripe")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_config")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_config")
	t.Setenv("STRIPE_API_BASE_URL", "http://127.0.0.1:12111")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.StripeSecretKey != "sk_test_config" {
		t.Errorf("secret key = %q, want the configured value", cfg.StripeSecretKey)
	}
	if cfg.StripeWebhookSecret != "whsec_config" {
		t.Errorf("webhook secret = %q, want the configured value", cfg.StripeWebhookSecret)
	}
	if cfg.StripeBaseURL != "http://127.0.0.1:12111" {
		t.Errorf("base url = %q, want the configured value", cfg.StripeBaseURL)
	}
}

func TestLoadWithoutStripeSettingsUsesTheFakeProvider(t *testing.T) {
	t.Setenv("PAYMENT_HOURLY_RATE", "5000")
	t.Setenv("PAYMENT_PROVIDER", "")
	t.Setenv("STRIPE_SECRET_KEY", "")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "")
	t.Setenv("STRIPE_API_BASE_URL", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PaymentProvider != "fake" {
		t.Errorf("provider = %q, want the fake default", cfg.PaymentProvider)
	}
	if cfg.StripeSecretKey != "" || cfg.StripeWebhookSecret != "" {
		t.Errorf("stripe settings = %q/%q, want both empty", cfg.StripeSecretKey, cfg.StripeWebhookSecret)
	}
	if cfg.StripeBaseURL != "" {
		t.Errorf("base url = %q, want the empty default", cfg.StripeBaseURL)
	}
}

func TestLoadRequiresRazorpaySettingsWhenRazorpayIsSelected(t *testing.T) {
	cases := []struct {
		name        string
		keyID       string
		keySecret   string
		webhookKey  string
		wantMissing string
	}{
		{name: "missing all", keyID: "", keySecret: "", webhookKey: "", wantMissing: "RAZORPAY_KEY_ID"},
		{name: "missing key id", keyID: "", keySecret: "rzp_secret_config", webhookKey: "whsec_rzp_config", wantMissing: "RAZORPAY_KEY_ID"},
		{name: "missing key secret", keyID: "rzp_test_config", keySecret: "", webhookKey: "whsec_rzp_config", wantMissing: "RAZORPAY_KEY_SECRET"},
		{name: "missing webhook secret", keyID: "rzp_test_config", keySecret: "rzp_secret_config", webhookKey: "", wantMissing: "RAZORPAY_WEBHOOK_SECRET"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PAYMENT_HOURLY_RATE", "5000")
			t.Setenv("PAYMENT_PROVIDER", "  RAZORPAY ")
			t.Setenv("RAZORPAY_KEY_ID", tc.keyID)
			t.Setenv("RAZORPAY_KEY_SECRET", tc.keySecret)
			t.Setenv("RAZORPAY_WEBHOOK_SECRET", tc.webhookKey)

			_, err := config.Load()
			if err == nil {
				t.Fatal("expected a configuration error")
			}
			if !strings.Contains(err.Error(), tc.wantMissing) {
				t.Errorf("error = %v, want it to name %s", err, tc.wantMissing)
			}
		})
	}
}

func TestLoadReadsRazorpaySettings(t *testing.T) {
	t.Setenv("PAYMENT_HOURLY_RATE", "5000")
	t.Setenv("PAYMENT_PROVIDER", "razorpay")
	t.Setenv("RAZORPAY_KEY_ID", "rzp_test_config")
	t.Setenv("RAZORPAY_KEY_SECRET", "rzp_secret_config")
	t.Setenv("RAZORPAY_WEBHOOK_SECRET", "whsec_rzp_config")
	t.Setenv("RAZORPAY_API_BASE_URL", "http://127.0.0.1:12112")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.RazorpayKeyID != "rzp_test_config" {
		t.Errorf("key id = %q, want the configured value", cfg.RazorpayKeyID)
	}
	if cfg.RazorpayKeySecret != "rzp_secret_config" {
		t.Errorf("key secret = %q, want the configured value", cfg.RazorpayKeySecret)
	}
	if cfg.RazorpayWebhookSecret != "whsec_rzp_config" {
		t.Errorf("webhook secret = %q, want the configured value", cfg.RazorpayWebhookSecret)
	}
	if cfg.RazorpayBaseURL != "http://127.0.0.1:12112" {
		t.Errorf("base url = %q, want the configured value", cfg.RazorpayBaseURL)
	}
}

func TestLoadNeverEchoesRazorpaySecrets(t *testing.T) {
	const keySecret = "rzp_secret_never_echo_this"
	const webhookSecret = "whsec_rzp_never_echo_this"

	t.Setenv("PAYMENT_HOURLY_RATE", "5000")
	t.Setenv("PAYMENT_PROVIDER", "razorpay")
	t.Setenv("RAZORPAY_KEY_ID", "rzp_test_never_echo")
	t.Setenv("RAZORPAY_KEY_SECRET", keySecret)
	t.Setenv("RAZORPAY_WEBHOOK_SECRET", webhookSecret)

	if _, err := config.Load(); err != nil {
		t.Fatalf("load with all three settings: %v", err)
	}

	// A failing load names the missing variable and neither value.
	t.Setenv("RAZORPAY_WEBHOOK_SECRET", "")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected a configuration error for the missing webhook secret")
	}
	if !strings.Contains(err.Error(), "RAZORPAY_WEBHOOK_SECRET") {
		t.Errorf("error = %v, want it to name RAZORPAY_WEBHOOK_SECRET", err)
	}
	if strings.Contains(err.Error(), keySecret) || strings.Contains(err.Error(), webhookSecret) {
		t.Errorf("error = %v, want neither secret in the message", err)
	}
}
