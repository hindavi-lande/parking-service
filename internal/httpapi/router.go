package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/techverito/parking-service/internal/service"
)

const requestTimeout = 10 * time.Second

// API wires the HTTP handlers to the services.
type API struct {
	lots     *service.LotService
	sessions *service.SessionService
	payments *service.PaymentService
	logger   *slog.Logger
}

// NewRouter builds the chi router with the same cross-cutting behaviour as the
// banking service: request ids, recovery, timeouts, structured logging and
// problem+json errors for every failure.
func NewRouter(lots *service.LotService, sessions *service.SessionService, payments *service.PaymentService, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	api := &API{lots: lots, sessions: sessions, payments: payments, logger: logger}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(requestLogger(logger))
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(requestTimeout))

	router.Get("/healthz", api.health)

	router.Route("/api/v1", func(r chi.Router) {
		r.Route("/lots", func(r chi.Router) {
			r.Get("/", api.listLots)
			r.Post("/", api.createLot)
			r.Get("/{lotID}", api.getLot)
			r.Post("/{lotID}/check-ins", api.checkIn)
		})
		r.Route("/sessions", func(r chi.Router) {
			r.Get("/{sessionID}", api.getSession)
			r.Post("/{sessionID}/checkout", api.checkout)
		})
		r.Route("/payments", func(r chi.Router) {
			r.Get("/{paymentID}", api.getPayment)
			r.Post("/webhooks/{provider}", api.paymentWebhook)
			// Stripe is told to call this fixed URL, so the provider is not part
			// of the path.
			r.Post("/stripe/webhook", api.stripeWebhook)
			// Razorpay is told to call this fixed URL too.
			r.Post("/razorpay/webhook", api.razorpayWebhook)
		})
	})

	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, "NOT_FOUND", "No route matches this request.")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "This method is not allowed for the route.")
	})

	return router
}
