package vault

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"testing"
)

func TestDerive(t *testing.T) {
	k := testKey(t, 7)
	a, b := k.Derive("sinjal csrf v1"), k.Derive("sinjal csrf v1")
	if len(a) != 32 || !bytes.Equal(a, b) {
		t.Fatalf("Derive not deterministic: %x %x", a, b)
	}
	if bytes.Equal(a, k.Derive("sinjal csrf v2")) {
		t.Error("different labels gave the same subkey")
	}
	if bytes.Equal(a, testKey(t, 8).Derive("sinjal csrf v1")) {
		t.Error("different master keys gave the same subkey")
	}
	if bytes.Equal(a, bytes.Repeat([]byte{7}, keySize)) {
		t.Error("subkey equals the master key")
	}
	// Pin the construction: HKDF-SHA256, no salt, label as info.
	want, err := hkdf.Key(sha256.New, bytes.Repeat([]byte{7}, keySize), nil, "sinjal csrf v1", 32)
	if err != nil || !bytes.Equal(a, want) {
		t.Errorf("Derive = %x, want HKDF-SHA256 %x (%v)", a, want, err)
	}
}
