// Package passkeytest is a software WebAuthn authenticator for tests. It
// plays the browser and the authenticator: it answers the options JSON a
// ceremony begins with by the JSON a browser would post to finish it.
//
// It is only imported by tests and is not part of the sinjal binary.
package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
)

// Authenticator data flags (WebAuthn §6.1).
const (
	flagUP = 0x01 // user present
	flagUV = 0x04 // user verified
	flagBE = 0x08 // backup eligible
	flagBS = 0x10 // backed up
	flagAT = 0x40 // attested credential data included
)

// Authenticator holds one ES256 credential. The exported fields change what
// the next response claims, so tests can produce the responses a server must
// refuse.
type Authenticator struct {
	RPID   string // hashed into the authenticator data
	Origin string // reported in clientDataJSON

	UV      bool   // user verification performed
	BE, BS  bool   // backup eligible / backed up
	Counter uint32 // signature counter sent with the next response

	CredentialID []byte
	UserHandle   []byte // learned at registration

	key *ecdsa.PrivateKey
}

// New returns an authenticator for the given relying party with a fresh
// credential, user verification on and a zero counter (like synced passkeys).
func New(rpID, origin string) *Authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	id := make([]byte, 16)
	rand.Read(id)
	return &Authenticator{RPID: rpID, Origin: origin, UV: true, CredentialID: id, key: key}
}

var b64 = base64.RawURLEncoding

func (a *Authenticator) flags() byte {
	f := byte(flagUP)
	for bit, on := range map[byte]bool{flagUV: a.UV, flagBE: a.BE, flagBS: a.BS} {
		if on {
			f |= bit
		}
	}
	return f
}

// authData is rpIdHash | flags | counter, the part both ceremonies share.
func (a *Authenticator) authData(extra byte) []byte {
	rp := sha256.Sum256([]byte(a.RPID))
	out := append(rp[:], a.flags()|extra)
	return binary.BigEndian.AppendUint32(out, a.Counter)
}

func (a *Authenticator) clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.Origin, "crossOrigin": false})
	return b
}

// cborBytes encodes a CBOR byte string header and its content.
func cborBytes(b []byte) []byte {
	switch n := len(b); {
	case n < 24:
		return append([]byte{0x40 | byte(n)}, b...)
	case n < 256:
		return append([]byte{0x58, byte(n)}, b...)
	default:
		return append([]byte{0x59, byte(n >> 8), byte(n)}, b...)
	}
}

func cborText(s string) []byte { return append([]byte{0x60 | byte(len(s))}, s...) } // len < 24

// coseKey is the credential public key as a COSE_Key map:
// {1: 2 (EC2), 3: -7 (ES256), -1: 1 (P-256), -2: x, -3: y}.
func (a *Authenticator) coseKey() []byte {
	point, err := a.key.PublicKey.Bytes() // 0x04 | X (32) | Y (32)
	if err != nil {
		panic(err)
	}
	out := []byte{0xa5, 0x01, 0x02, 0x03, 0x26, 0x20, 0x01, 0x21}
	out = append(out, cborBytes(point[1:33])...)
	out = append(out, 0x22)
	return append(out, cborBytes(point[33:])...)
}

// Create answers creation options ({"publicKey": {...}}) with a
// registration response using attestation format "none".
func (a *Authenticator) Create(options []byte) ([]byte, error) {
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, err
	}
	handle, err := b64.DecodeString(o.PublicKey.User.ID)
	if err != nil || o.PublicKey.Challenge == "" {
		return nil, errors.New("passkeytest: creation options lack a challenge or user id")
	}
	a.UserHandle = handle

	auth := a.authData(flagAT)
	auth = append(auth, make([]byte, 16)...) // AAGUID: zero
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(a.CredentialID)))
	auth = append(auth, a.CredentialID...)
	auth = append(auth, a.coseKey()...)

	// {"fmt": "none", "attStmt": {}, "authData": bytes}
	att := []byte{0xa3}
	att = append(att, cborText("fmt")...)
	att = append(att, cborText("none")...)
	att = append(att, cborText("attStmt")...)
	att = append(att, 0xa0)
	att = append(att, cborText("authData")...)
	att = append(att, cborBytes(auth)...)

	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.CredentialID), "rawId": b64.EncodeToString(a.CredentialID), "type": "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", o.PublicKey.Challenge)),
			"attestationObject": b64.EncodeToString(att),
			"transports":        []string{"internal"},
		},
	})
}

// Get answers request options ({"publicKey": {...}}) with a signed
// assertion for the credential, including the user handle.
func (a *Authenticator) Get(options []byte) ([]byte, error) {
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, err
	}
	if o.PublicKey.Challenge == "" {
		return nil, errors.New("passkeytest: request options lack a challenge")
	}
	auth := a.authData(0)
	client := a.clientData("webauthn.get", o.PublicKey.Challenge)
	clientHash := sha256.Sum256(client)
	digest := sha256.Sum256(append(append([]byte{}, auth...), clientHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.CredentialID), "rawId": b64.EncodeToString(a.CredentialID), "type": "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(client),
			"authenticatorData": b64.EncodeToString(auth),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(a.UserHandle),
		},
	})
}
