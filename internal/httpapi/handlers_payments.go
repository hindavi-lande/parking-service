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
	// ClientSecret and FailureMessage are optional provider details: the first
	// is the single-use secret the client confirms the payment with, the second
	// is why a failed payment was declined. Both are omitted when empty, so a
	// payment from the fake provider keeps its existing shape.
	ClientSecret   string `json:"clientSecret,omitempty"`
	FailureMessage string `json:"failureMessage,omitempty"`
	// OrderID and KeyID are the optional provider checkout details: the first is
	// the provider's order identifier, the second the publishable key the client
	// launches the provider checkout with. Both are omitted when empty, so a
	// payment from the fake or Stripe provider keeps its existing shape.
	OrderID string `json:"orderId,omitempty"`
	KeyID   string `json:"keyId,omitempty"`
}

func toPaymentResponse(p domain.Payment) paymentResponse {
	return paymentResponse{
		ID:             p.ID,
		SessionID:      p.SessionID,
		LotID:          p.LotID,
		Amount:         p.Amount,
		Currency:       p.Currency,
		Provider:       p.Provider,
		Status:         string(p.Status),
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
		ClientSecret:   p.ClientSecret,
		FailureMessage: p.FailureMessage,
		OrderID:        p.ProviderOrderID,
		KeyID:          p.ProviderKeyID,
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

// stripeWebhook handles the callback Stripe is configured to send to the fixed
// /api/v1/payments/stripe/webhook URL. Like paymentWebhook it reads the body
// once and passes it on untouched, because the signature is verified over the
// raw bytes; a signature that does not verify comes back as a validation error
// and answers 400 without changing anything.
func (a *API) stripeWebhook(w http.ResponseWriter, r *http.Request) {
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

	settled, err := a.payments.HandleStripeWebhook(r.Context(), payment.WebhookRequest{
		Body:   body,
		Header: header,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentResponse(settled))
}

// razorpayWebhook handles the callback Razorpay is configured to send to the
// fixed /api/v1/payments/razorpay/webhook URL. Like stripeWebhook it reads the
// body once and passes it on untouched, because X-Razorpay-Signature is
// verified over the raw bytes; a signature that does not verify, or a captured
// event whose amount or currency does not match the payment, comes back as a
// validation error and answers 400 without changing the payment.
func (a *API) razorpayWebhook(w http.ResponseWriter, r *http.Request) {
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

	settled, err := a.payments.HandleRazorpayWebhook(r.Context(), payment.WebhookRequest{
		Body:   body,
		Header: header,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentResponse(settled))
}
