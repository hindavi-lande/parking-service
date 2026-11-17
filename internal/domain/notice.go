package domain

import "time"

// CheckInNotice is the fact that a vehicle was checked in and the driver may be
// told about it. It carries no delivery details: how the message reaches the
// driver is the notifier's concern.
type CheckInNotice struct {
	SessionID   string
	LotName     string
	Plate       string
	Phone       string
	CheckedInAt time.Time
}
