package domain

import (
	"errors"
	"fmt"
)

var (
	// ErrNotFound means the requested lot or session does not exist.
	ErrNotFound = errors.New("not found")
	// ErrLotFull means the lot has no free capacity for another vehicle.
	ErrLotFull = errors.New("lot is full")
	// ErrSessionNotActive means the session has already been checked out.
	ErrSessionNotActive = errors.New("session is not active")
	// ErrPaymentNotPending means the payment already reached a terminal status
	// and cannot change again.
	ErrPaymentNotPending = errors.New("payment is not pending")
	// ErrPaymentExists means the session already has a payment.
	ErrPaymentExists = errors.New("payment already exists for session")
	// ErrProviderUnavailable means the payment provider could not be reached or
	// answered with a server error, so the payment is still PENDING and the
	// call can be retried. The HTTP layer maps it to 502.
	ErrProviderUnavailable = errors.New("payment provider unavailable")
)

// ValidationError describes a rejected input field. Services return it for
// values the caller can fix; the HTTP layer maps it to 400.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s %s", e.Field, e.Message)
}
