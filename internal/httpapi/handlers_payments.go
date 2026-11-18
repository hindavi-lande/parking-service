package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/techverito/parking-service/internal/domain"
	"github.com/techverito/parking-service/internal/payment"
)

// paymentResponse is the wire shape of a payment. Amount is an integer in minor
// units of Currency, never a float.
type paymentResponse struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	LotID     string    `json:"lotId"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	Provider  string    `json:"provider"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func toPaymentResponse(p domain.Payment) paymentResponse {
	return paymentResponse{
		ID:        p.ID,
		SessionID: p.SessionID,
		LotID:     p.LotID,
		Amount:    p.Amount,
		Currency:  p.Currency,
		Provider:  p.Provider,
		Status:    string(p.Status),
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

func (a *API) getPayment(w http.ResponseWriter, r *http.Request) {
	stored, err := a.payments.Get(r.Context(), chi.URLParam(r, "paymentID"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentResponse(stored))
}

// paymentWebhook hands the untouched callback body to the provider named in the
// path. Headers are copied into a provider-neutral map with lower-cased names,
// so a provider can verify a signature without this package exposing net/http
// types to the service.
func (a *API) paymentWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.badRequest(w, r, fmt.Errorf("invalid request body: %w", err))
		return
	}

	header := make(map[string]string, len(r.Header))
	for name, values := range r.Header {
		header[strings.ToLower(name)] = strings.Join(values, ", ")
	}

	settled, err := a.payments.HandleWebhook(r.Context(), chi.URLParam(r, "provider"), payment.WebhookRequest{
		Body:   body,
		Header: header,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentResponse(settled))
}
