package auth

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Vectors computed independently with Node's crypto.argon2Sync (OpenSSL).
var vectors = []struct {
	name, password, encoded string
}{
	{
		name:     "current parameters",
		password: "correct horse battery staple",
		encoded:  "$argon2id$v=19$m=19456,t=2,p=1$c2luamFsLXRlc3Qtc2FsdA$pzAFPJCVXXP3iwvcjDvs3wua3bdIu+Bgs5RRkF44vvs",
	},
	{
		name:     "other parameters, unicode password",
		password: "pässwörd 🔑",
		encoded:  "$argon2id$v=19$m=64,t=3,p=2$MDEyMzQ1Njc4OWFiY2RlZg$u75395toF6FqIC23JjozjQ",
	},
}

func TestKnownVectors(t *testing.T) {
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			ok, _, err := VerifyPassword(v.password, v.encoded)
			if err != nil || !ok {
				t.Fatalf("VerifyPassword = %v, %v", ok, err)
			}
			// Re-encoding the decoded parts reproduces the string exactly.
			p, salt, _, err := decode(v.encoded)
			if err != nil {
				t.Fatal(err)
			}
			if got := encode(p, salt, derive(v.password, salt, p)); got != v.encoded {
				t.Fatalf("encode = %s\nwant      %s", got, v.encoded)
			}
		})
	}
}

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)
	if !re.MatchString(h) {
		t.Fatalf("hash %q does not match the PHC format", h)
	}
	ok, rehash, err := VerifyPassword("s3cret", h)
	if err != nil || !ok || rehash {
		t.Fatalf("correct password: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	for _, wrong := range []string{"", "s3cre", "s3cret ", "S3cret", "s3cret\x00"} {
		if ok, rehash, err := VerifyPassword(wrong, h); err != nil || ok || rehash {
			t.Errorf("wrong password %q: ok=%v rehash=%v err=%v", wrong, ok, rehash, err)
		}
	}
}

func TestSaltsDiffer(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("two hashes of the same password are identical")
	}
	if strings.Split(a, "$")[4] == strings.Split(b, "$")[4] {
		t.Fatal("salt reused")
	}
}

func TestRehashOnParamChange(t *testing.T) {
	old, err := HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}

	saved := currentParams
	t.Cleanup(func() { currentParams = saved })

	for name, p := range map[string]Params{
		"memory":  {Memory: 12 * 1024, Time: 3, Threads: 1, SaltLen: 16, KeyLen: 32},
		"time":    {Memory: 19 * 1024, Time: 3, Threads: 1, SaltLen: 16, KeyLen: 32},
		"threads": {Memory: 19 * 1024, Time: 2, Threads: 2, SaltLen: 16, KeyLen: 32},
		"salt":    {Memory: 19 * 1024, Time: 2, Threads: 1, SaltLen: 32, KeyLen: 32},
		"key":     {Memory: 19 * 1024, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 64},
	} {
		currentParams = p
		ok, rehash, err := VerifyPassword("pw", old)
		if err != nil || !ok || !rehash {
			t.Errorf("%s changed: ok=%v rehash=%v err=%v, want true true nil", name, ok, rehash, err)
		}
		// A wrong password never asks for a rehash.
		if ok, rehash, _ := VerifyPassword("nope", old); ok || rehash {
			t.Errorf("%s changed, wrong password: ok=%v rehash=%v", name, ok, rehash)
		}
		// The new hash uses the new parameters and needs no further rehash.
		fresh, err := HashPassword("pw")
		if err != nil {
			t.Fatal(err)
		}
		if ok, rehash, err := VerifyPassword("pw", fresh); err != nil || !ok || rehash {
			t.Errorf("%s: fresh hash ok=%v rehash=%v err=%v", name, ok, rehash, err)
		}
	}
}

func TestInvalidHashes(t *testing.T) {
	const salt, key = "c2luamFsLXRlc3Qtc2FsdA", "pzAFPJCVXXP3iwvcjDvs3wua3bdIu+Bgs5RRkF44vvs"
	cases := map[string]string{
		"empty":            "",
		"plain text":       "s3cret",
		"bcrypt":           "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
		"argon2i":          "$argon2i$v=19$m=19456,t=2,p=1$" + salt + "$" + key,
		"argon2d":          "$argon2d$v=19$m=19456,t=2,p=1$" + salt + "$" + key,
		"version 16":       "$argon2id$v=16$m=19456,t=2,p=1$" + salt + "$" + key,
		"no version":       "$argon2id$m=19456,t=2,p=1$" + salt + "$" + key,
		"missing leading":  "argon2id$v=19$m=19456,t=2,p=1$" + salt + "$" + key,
		"extra field":      "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$" + key + "$x",
		"params reordered": "$argon2id$v=19$t=2,m=19456,p=1$" + salt + "$" + key,
		"params missing":   "$argon2id$v=19$m=19456,t=2$" + salt + "$" + key,
		"params extra":     "$argon2id$v=19$m=19456,t=2,p=1,k=1$" + salt + "$" + key,
		"zero time":        "$argon2id$v=19$m=19456,t=0,p=1$" + salt + "$" + key,
		"zero threads":     "$argon2id$v=19$m=19456,t=2,p=0$" + salt + "$" + key,
		"memory too large": "$argon2id$v=19$m=262145,t=2,p=1$" + salt + "$" + key,
		"memory overflow":  "$argon2id$v=19$m=4294967296,t=2,p=1$" + salt + "$" + key,
		"time too large":   "$argon2id$v=19$m=19456,t=17,p=1$" + salt + "$" + key,
		"threads too many": "$argon2id$v=19$m=19456,t=2,p=9$" + salt + "$" + key,
		"memory < 8p":      "$argon2id$v=19$m=15,t=2,p=2$" + salt + "$" + key,
		"signed param":     "$argon2id$v=19$m=+19456,t=2,p=1$" + salt + "$" + key,
		"spaced param":     "$argon2id$v=19$m= 19456,t=2,p=1$" + salt + "$" + key,
		"hex param":        "$argon2id$v=19$m=0x4c00,t=2,p=1$" + salt + "$" + key,
		"empty param":      "$argon2id$v=19$m=,t=2,p=1$" + salt + "$" + key,
		"padded salt":      "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "==$" + key,
		"bad salt b64":     "$argon2id$v=19$m=19456,t=2,p=1$c2luamFs!XRlc3Qtc2FsdA$" + key,
		"short salt":       "$argon2id$v=19$m=19456,t=2,p=1$YWJj$" + key,
		"empty salt":       "$argon2id$v=19$m=19456,t=2,p=1$$" + key,
		"short key":        "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$YWJjZGVmZ2g",
		"empty key":        "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$",
		"url b64 key":      "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$pzAFPJCVXXP3iwvcjDvs3wua3bdIu-Bgs5RRkF44vvs",
	}
	for name, enc := range cases {
		ok, rehash, err := VerifyPassword("correct horse battery staple", enc)
		if !errors.Is(err, ErrInvalidHash) || ok || rehash {
			t.Errorf("%s: ok=%v rehash=%v err=%v, want ErrInvalidHash", name, ok, rehash, err)
		}
	}
}

func TestConcurrentHashesAreBounded(t *testing.T) {
	saved := currentParams
	currentParams = Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	t.Cleanup(func() { currentParams = saved })

	// Occupy every slot: a further hash must wait.
	for range maxConcurrentHashes {
		hashSlots <- struct{}{}
	}
	done := make(chan struct{})
	go func() {
		HashPassword("x")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("hash ran while all slots were taken")
	case <-time.After(50 * time.Millisecond):
	}

	<-hashSlots // free one slot
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hash did not run after a slot was freed")
	}
	<-hashSlots
	if len(hashSlots) != 0 {
		t.Fatalf("%d hash slots leaked", len(hashSlots))
	}
}

func BenchmarkHashPassword(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := HashPassword("correct horse battery staple"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyPassword(b *testing.B) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if ok, _, _ := VerifyPassword("correct horse battery staple", h); !ok {
			b.Fatal("no match")
		}
	}
}
