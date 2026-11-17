package domain

import "time"

// CurrencyINR is the only currency the service charges in. It is a constant
// rather than a setting; the amount is always in minor units of it.
const CurrencyINR = "INR"

// PaymentStatus tells where a payment stands. PENDING is the only status a
// provider callback can move away from.
type PaymentStatus string

const (
	PaymentPending PaymentStatus = "PENDING"
	PaymentPaid    PaymentStatus = "PAID"
	PaymentFailed  PaymentStatus = "FAILED"
)

// IsTerminal reports whether the payment has reached a status it can never
// leave. PAID and FAILED are terminal; PENDING is not.
func (p Payment) IsTerminal() bool {
	return p.Status == PaymentPaid || p.Status == PaymentFailed
}

// Payment is the charge for one finished session. Amount is an integer in
// minor units of Currency (5000 is INR 50.00), never a float.
type Payment struct {
	ID        string
	SessionID string
	LotID     string
	Amount    int64
	Currency  string
	Provider  string
	Status    PaymentStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}
