// Package auth holds authentication primitives (docs/13_AUTH_SECURITY.md).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are Argon2id cost parameters. Memory is in KiB.
type Params struct {
	Memory  uint32
	Time    uint32
	Threads uint8
	SaltLen int
	KeyLen  int
}

// currentParams is the OWASP baseline (m=19 MiB, t=2, p=1); benchmark and
// RAM rationale in docs/13_AUTH_SECURITY.md. A var so tests can simulate a
// parameter change.
var currentParams = Params{Memory: 19 * 1024, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}

// Bounds applied to parameters read back from the database, so a corrupt or
// tampered hash cannot make one verification allocate gigabytes or spin.
const (
	maxMemory  = 256 * 1024 // KiB
	maxTime    = 16
	maxThreads = 8
	minSaltLen = 8
	maxSaltLen = 64
	minKeyLen  = 16
	maxKeyLen  = 64
)

// Every hash allocates Params.Memory; at most this many run at once, which
// bounds the transient memory of a login burst to about 2 × 19 MiB.
const maxConcurrentHashes = 2

var hashSlots = make(chan struct{}, maxConcurrentHashes)

// ErrInvalidHash means a stored hash is not a well-formed, supported Argon2id
// PHC string. It never means "wrong password".
var ErrInvalidHash = errors.New("auth: invalid password hash")

var b64 = base64.RawStdEncoding.Strict()

// HashPassword hashes password with the current parameters and returns a PHC
// string: $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash> (unpadded base64).
func HashPassword(password string) (string, error) {
	p := currentParams
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return encode(p, salt, derive(password, salt, p)), nil
}

// VerifyPassword reports whether password matches the encoded hash, using
// the parameters stored in it. needsRehash is true on a match whose
// parameters differ from the current ones; the caller should then store
// HashPassword(password) (rehash on login). A malformed hash returns
// ErrInvalidHash.
func VerifyPassword(password, encoded string) (ok, needsRehash bool, err error) {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	got := derive(password, salt, p)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	return true, p != currentParams, nil
}

func derive(password string, salt []byte, p Params) []byte {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(p.KeyLen))
}

func encode(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

func decode(encoded string) (Params, []byte, []byte, error) {
	var p Params
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, nil, nil, ErrInvalidHash
	}

	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return p, nil, nil, ErrInvalidHash
	}
	m, okM := param(fields[0], "m=", maxMemory)
	t, okT := param(fields[1], "t=", maxTime)
	th, okP := param(fields[2], "p=", maxThreads)
	if !okM || !okT || !okP || m < 8*th { // Argon2 requires m >= 8p
		return p, nil, nil, ErrInvalidHash
	}

	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < minSaltLen || len(salt) > maxSaltLen {
		return p, nil, nil, ErrInvalidHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < minKeyLen || len(key) > maxKeyLen {
		return p, nil, nil, ErrInvalidHash
	}

	p = Params{Memory: uint32(m), Time: uint32(t), Threads: uint8(th), SaltLen: len(salt), KeyLen: len(key)}
	return p, salt, key, nil
}

// param parses "<prefix><n>" with 1 <= n <= max, digits only.
func param(s, prefix string, max uint64) (uint64, bool) {
	digits, found := strings.CutPrefix(s, prefix)
	if !found || digits == "" || strings.TrimLeft(digits, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseUint(digits, 10, 32)
	if err != nil || n < 1 || n > max {
		return 0, false
	}
	return n, true
}
