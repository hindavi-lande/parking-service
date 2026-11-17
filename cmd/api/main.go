package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/techverito/parking-service/internal/config"
	"github.com/techverito/parking-service/internal/httpapi"
	"github.com/techverito/parking-service/internal/notify"
	"github.com/techverito/parking-service/internal/service"
	"github.com/techverito/parking-service/internal/store/memory"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	lots := memory.NewLotStore()
	sessions := memory.NewSessionStore()

	lotService := service.NewLotService(lots, time.Now)
	sessionService := service.NewSessionService(lots, sessions, time.Now, newNotifier(cfg, logger), logger)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.NewRouter(lotService, sessionService, logger),
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

// newNotifier picks the messaging provider: Twilio once all three settings are
// present, the log-only notifier otherwise, so local development and tests need
// no account. The auth token is never logged.
func newNotifier(cfg config.Config, logger *slog.Logger) notify.Notifier {
	if cfg.TwilioConfigured() {
		logger.Info("using the twilio notifier", "from_number", cfg.TwilioFromNumber)
		return notify.NewTwilioNotifier(cfg.TwilioAccountSID, cfg.TwilioAuthToken, cfg.TwilioFromNumber, logger)
	}
	logger.Info("using the log-only notifier", "reason", "twilio settings are not configured")
	return notify.NewLogNotifier(logger)
}
