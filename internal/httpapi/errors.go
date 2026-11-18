package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/techverito/parking-service/internal/domain"
)

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// fail maps domain errors to the HTTP problem responses. Anything it does not
// recognise is logged and reported as an internal error without leaking details.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	var validation *domain.ValidationError

	switch {
	case errors.As(err, &validation):
		writeProblem(w, r, http.StatusBadRequest, "VALIDATION_FAILED", validation.Error())
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "NOT_FOUND", "The requested resource does not exist.")
	case errors.Is(err, domain.ErrLotFull):
		writeProblem(w, r, http.StatusConflict, "LOT_FULL", "The lot has no free capacity.")
	case errors.Is(err, domain.ErrSessionNotActive):
		writeProblem(w, r, http.StatusConflict, "SESSION_NOT_ACTIVE", "The session has already been checked out.")
	case errors.Is(err, domain.ErrPaymentNotPending):
		writeProblem(w, r, http.StatusConflict, "PAYMENT_NOT_PENDING", "The payment has already reached a final status.")
	case errors.Is(err, domain.ErrProviderUnavailable):
		writeProblem(w, r, http.StatusBadGateway, "PROVIDER_ERROR", "The payment provider is unavailable; the payment is still PENDING and the checkout can be retried.")
	default:
		a.logger.Error("request failed",
			"error", err,
			"method", r.Method,
			"path", r.URL.Path,
			"request_id", middleware.GetReqID(r.Context()),
		)
		writeProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "An unexpected error occurred.")
	}
}

func (a *API) badRequest(w http.ResponseWriter, r *http.Request, err error) {
	writeProblem(w, r, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
}
