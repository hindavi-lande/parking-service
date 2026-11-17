package domain

import "time"

// SessionKind is the type of space a vehicle needs. A standard space cannot be
// used by an electric vehicle unless the driver chooses to.
type SessionKind string

const (
	KindStandard SessionKind = "STANDARD"
	KindEV       SessionKind = "EV"
)

// SessionStatus tells whether a vehicle is still parked.
type SessionStatus string

const (
	SessionActive     SessionStatus = "ACTIVE"
	SessionCheckedOut SessionStatus = "CHECKED_OUT"
)

// Session is one vehicle parked in one lot between check-in and checkout.
type Session struct {
	ID           string
	LotID        string
	Plate        string
	Kind         SessionKind
	Status       SessionStatus
	CheckedInAt  time.Time
	CheckedOutAt *time.Time
}
