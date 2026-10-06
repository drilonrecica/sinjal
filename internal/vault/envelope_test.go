package vault

import (
	"bytes"
	"errors"
	"testing"
)

func testKey(t testing.TB, fill byte) *Key {
	t.Helper()
	k, err := newKey(bytes.Repeat([]byte{fill}, keySize))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

var ctxA = Context{Table: "users", Column: "totp_secret_enc", RowID: "u1"}

func TestEnvelopeRoundTrip(t *testing.T) {
	k := testKey(t, 1)
	for _, pt := range [][]byte{nil, {}, []byte("x"), []byte("JBSWY3DPEHPK3PXP"), bytes.Repeat([]byte{0xff}, 4096)} {
		env := k.Seal(ctxA, pt)
		if len(env) != 1+nonceSize+len(pt)+tagSize {
			t.Fatalf("envelope length %d for %d-byte plaintext", len(env), len(pt))
		}
		if env[0] != version1 {
			t.Fatalf("version byte = %#x", env[0])
		}
		// Only meaningful for longer values: one byte turns up in ~29
		// random bytes about one time in ten.
		if len(pt) >= 8 && bytes.Contains(env, pt) {
			t.Fatal("plaintext appears in the envelope")
		}
		got, err := k.Open(ctxA, env)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("round trip = %q, want %q", got, pt)
		}
	}
}

func TestEnvelopeSurvivesKeyReload(t *testing.T) {
	env := testKey(t, 1).Seal(ctxA, []byte("secret"))
	got, err := testKey(t, 1).Open(ctxA, env)
	if err != nil || string(got) != "secret" {
		t.Fatalf("Open with a reloaded key = %q, %v", got, err)
	}
}

func TestEnvelopeTamper(t *testing.T) {
	k := testKey(t, 1)
	env := k.Seal(ctxA, []byte("secret value"))
	// Every byte after the version: nonce, ciphertext and tag.
	for i := 1; i < len(env); i++ {
		bad := bytes.Clone(env)
		bad[i] ^= 0x01
		if _, err := k.Open(ctxA, bad); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("flipped byte %d: err = %v, want ErrDecrypt", i, err)
		}
	}
	// Appended or removed trailing bytes.
	if _, err := k.Open(ctxA, append(bytes.Clone(env), 0)); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("appended byte: err = %v", err)
	}
	if _, err := k.Open(ctxA, env[:len(env)-1]); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("dropped byte: err = %v", err)
	}
}

func TestEnvelopeWrongContext(t *testing.T) {
	k := testKey(t, 1)
	env := k.Seal(ctxA, []byte("secret"))
	for name, c := range map[string]Context{
		"table":     {Table: "monitors", Column: ctxA.Column, RowID: ctxA.RowID},
		"column":    {Table: ctxA.Table, Column: "config_enc", RowID: ctxA.RowID},
		"row":       {Table: ctxA.Table, Column: ctxA.Column, RowID: "u2"},
		"empty":     {},
		"shifted":   {Table: "user", Column: "stotp_secret_enc", RowID: "u1"},
		"row empty": {Table: ctxA.Table, Column: ctxA.Column},
	} {
		if _, err := k.Open(c, env); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: err = %v, want ErrDecrypt", name, err)
		}
	}
}

func TestEnvelopeContextIsUnambiguous(t *testing.T) {
	a := Context{Table: "ab", Column: "c", RowID: "d"}
	b := Context{Table: "a", Column: "bc", RowID: "d"}
	if bytes.Equal(a.aad(version1), b.aad(version1)) {
		t.Fatal("different contexts produce the same AAD")
	}
	k := testKey(t, 1)
	if _, err := k.Open(b, k.Seal(a, []byte("x"))); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("err = %v, want ErrDecrypt", err)
	}
}

func TestEnvelopeWrongKey(t *testing.T) {
	env := testKey(t, 1).Seal(ctxA, []byte("secret"))
	if _, err := testKey(t, 2).Open(ctxA, env); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("err = %v, want ErrDecrypt", err)
	}
}

func TestEnvelopeUnknownVersion(t *testing.T) {
	k := testKey(t, 1)
	env := k.Seal(ctxA, []byte("secret"))
	for _, v := range []byte{0x00, 0x02, 0xff} {
		bad := bytes.Clone(env)
		bad[0] = v
		if _, err := k.Open(ctxA, bad); !errors.Is(err, ErrUnknownVersion) {
			t.Errorf("version %#x: err = %v, want ErrUnknownVersion", v, err)
		}
	}
}

func TestEnvelopeMalformed(t *testing.T) {
	k := testKey(t, 1)
	env := k.Seal(ctxA, nil) // shortest valid envelope
	if _, err := k.Open(ctxA, nil); !errors.Is(err, ErrMalformed) {
		t.Errorf("nil: err = %v, want ErrMalformed", err)
	}
	for n := 1; n < len(env); n++ {
		if _, err := k.Open(ctxA, env[:n]); !errors.Is(err, ErrMalformed) {
			t.Errorf("%d bytes: err = %v, want ErrMalformed", n, err)
		}
	}
}

func TestEnvelopeNonceUniqueness(t *testing.T) {
	k := testKey(t, 1)
	const n = 10000
	seen := make(map[[nonceSize]byte]struct{}, n)
	var first []byte
	for i := 0; i < n; i++ {
		env := k.Seal(ctxA, []byte("same plaintext"))
		var nonce [nonceSize]byte
		copy(nonce[:], env[1:1+nonceSize])
		if _, dup := seen[nonce]; dup {
			t.Fatalf("nonce repeated after %d seals", i)
		}
		seen[nonce] = struct{}{}
		if i == 0 {
			first = env
		} else if bytes.Equal(env[1+nonceSize:], first[1+nonceSize:]) {
			t.Fatal("identical ciphertext for the same plaintext")
		}
	}
}

func BenchmarkSeal(b *testing.B) {
	k := testKey(b, 1)
	pt := bytes.Repeat([]byte("s"), 64)
	b.ReportAllocs()
	for b.Loop() {
		k.Seal(ctxA, pt)
	}
}

func BenchmarkOpen(b *testing.B) {
	k := testKey(b, 1)
	env := k.Seal(ctxA, bytes.Repeat([]byte("s"), 64))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := k.Open(ctxA, env); err != nil {
			b.Fatal(err)
		}
	}
}
