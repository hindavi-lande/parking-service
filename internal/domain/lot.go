// Package domain holds the parking model and the errors the services return.
// It has no knowledge of HTTP, storage or any third-party client.
package domain

import "time"

// Lot is a physical parking location with a fixed number of spaces.
type Lot struct {
	ID       string
	Name     string
	Address  string
	Capacity int
	// HourlyRate is the rate in minor units the operator set on the lot. A nil
	// value means the configured default rate applies; zero is a valid rate and
	// means free parking.
	HourlyRate *int
	CreatedAt  time.Time
}
