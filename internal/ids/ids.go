// Package ids generates primary keys for TEXT id columns.
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a random 128-bit identifier as 32 lowercase hex characters.
func New() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails on supported platforms
	return hex.EncodeToString(b[:])
}
