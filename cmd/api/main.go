package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/techverito/parking-service/internal/config"
	"github.com/techverito/parking-service/internal/httpapi"
	"github.com/techverito/parking-service/internal/notify"
	"github.com/techverito/parking-service/internal/payments"
	"github.com/techverito/parking-service/internal/service"
	"github.com/techverito/parking-service/internal/store/memory"
)

func main() {
	loadDotEnv(".env")

	cfg, err := config.Load()
	if err != nil {
		slog.Default().Error("load configuration", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	lots := memory.NewLotStore()
	sessions := memory.NewSessionStore()
	paymentStore := memory.NewPaymentStore()
	notifier := notify.FromSettings(notify.TwilioSettings{
		AccountSID: cfg.TwilioAccountSID,
		AuthToken:  cfg.TwilioAuthToken,
		FromNumber: cfg.TwilioFromNumber,
	}, logger)

	provider, err := payments.FromSettings(payments.Settings{
		Name:                  cfg.PaymentProvider,
		StripeSecretKey:       cfg.StripeSecretKey,
		StripeWebhookSecret:   cfg.StripeWebhookSecret,
		StripeBaseURL:         cfg.StripeBaseURL,
		RazorpayKeyID:         cfg.RazorpayKeyID,
		RazorpayKeySecret:     cfg.RazorpayKeySecret,
		RazorpayWebhookSecret: cfg.RazorpayWebhookSecret,
		RazorpayBaseURL:       cfg.RazorpayBaseURL,
	})
	if err != nil {
		logger.Error("select payment provider", "error", err)
		os.Exit(1)
	}

	paymentService := service.NewPaymentService(paymentStore, lots, provider, cfg.PaymentHourlyRate, time.Now)

	server := &http.Server{
		Addr: cfg.Addr,
		Handler: httpapi.NewRouter(
			service.NewLotService(lots, time.Now),
			service.NewSessionService(lots, sessions, paymentService, time.Now, notifier, logger),
			paymentService,
			logger,
		),
		ReadHeaderTimeout: cfg.ReadTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.Addr, "env", cfg.Env)
		serverErrors <- server.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}

// loadDotEnv reads the optional .env file the README describes and sets only the
// variables that are not already exported, so a real environment always wins.
// A missing file is not an error: config.Load still rejects a missing
// PAYMENT_HOURLY_RATE.
func loadDotEnv(path string) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return
	}

	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			continue
		}
	}
}
