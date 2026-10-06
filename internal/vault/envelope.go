package vault

import (
	"encoding/binary"
	"errors"
)

// Envelope format v1:
//
//	0x01 | nonce (12 B) | AES-256-GCM ciphertext | tag (16 B)
//
// The nonce is drawn from crypto/rand for every Seal by
// cipher.NewGCMWithRandomNonce. Random 96-bit nonces stay safe for far more
// than 2^32 seals under one key, which this application never approaches.
const version1 byte = 0x01

const (
	nonceSize = 12
	tagSize   = 16
)

var (
	// ErrMalformed means the envelope is too short to be valid.
	ErrMalformed = errors.New("vault: malformed envelope")
	// ErrUnknownVersion means the envelope's version byte is not supported.
	ErrUnknownVersion = errors.New("vault: unknown envelope version")
	// ErrDecrypt covers tampering, a wrong context and a wrong key alike;
	// they are deliberately indistinguishable.
	ErrDecrypt = errors.New("vault: cannot decrypt envelope")
)

// Context binds an envelope to where it is stored, so a value copied to
// another row or column fails to decrypt. It is authenticated, not encrypted.
type Context struct {
	Table, Column, RowID string
}

// aad encodes the version byte and each context field, length-prefixed so
// that {"ab","c"} and {"a","bc"} differ.
func (c Context) aad(version byte) []byte {
	b := make([]byte, 0, 1+3*binary.MaxVarintLen64+len(c.Table)+len(c.Column)+len(c.RowID))
	b = append(b, version)
	for _, f := range [...]string{c.Table, c.Column, c.RowID} {
		b = binary.AppendUvarint(b, uint64(len(f)))
		b = append(b, f...)
	}
	return b
}

// Seal encrypts plaintext for storage at ctx and returns a v1 envelope.
func (k *Key) Seal(ctx Context, plaintext []byte) []byte {
	out := make([]byte, 1, 1+nonceSize+len(plaintext)+tagSize)
	out[0] = version1
	return k.aead.Seal(out, nil, plaintext, ctx.aad(version1))
}

// Open decrypts an envelope produced by Seal with the same key and ctx.
func (k *Key) Open(ctx Context, envelope []byte) ([]byte, error) {
	if len(envelope) == 0 {
		return nil, ErrMalformed
	}
	if envelope[0] != version1 {
		return nil, ErrUnknownVersion
	}
	if len(envelope) < 1+nonceSize+tagSize {
		return nil, ErrMalformed
	}
	pt, err := k.aead.Open(nil, nil, envelope[1:], ctx.aad(version1))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}
