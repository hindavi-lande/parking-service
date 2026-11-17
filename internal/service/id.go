// Package service holds the business rules. It depends on store interfaces and
// the domain package only, never on HTTP or on a database driver.
package service

import "crypto/rand"

// newID returns an unguessable, URL-safe identifier. It replaces the database
// generated primary keys the banking service used.
func newID() string {
	return rand.Text()
}
