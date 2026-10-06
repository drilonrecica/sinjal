package vault

import (
	"crypto/hkdf"
	"crypto/sha256"
)

// Derive returns a 32-byte subkey for one purpose, for example
// "sinjal csrf v1". It is HKDF-Expand over the master key with label as the
// info string: subkeys for different labels are independent, and none of
// them reveals the master key. Labels are fixed strings in the code; a new
// version suffix rotates a subkey.
func (k *Key) Derive(label string) []byte {
	out, err := hkdf.Expand(sha256.New, k.prk, label, 32)
	if err != nil {
		panic("vault: hkdf expand: " + err.Error()) // only fails for lengths > 255*32
	}
	return out
}
