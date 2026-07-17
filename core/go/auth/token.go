package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Token format (GitHub-PAT style, so secret scanners can catch a leaked token
// by its fixed prefix):
//
//	gtk_<id>_<secret>
//
// where id is 8 lowercase hex chars (4 crypto/rand bytes) and secret is 32
// crypto/rand bytes in unpadded base64url. The id is a public lookup handle
// stored in the clear; the secret is the entropy. Only SHA-256(secret) is
// persisted.
//
// Why SHA-256 and not argon2/bcrypt: those exist to slow brute force of
// LOW-entropy human passwords. This secret is 32 bytes (256 bits) from a CSPRNG
// — it cannot be brute forced regardless of hash speed, so a single SHA-256 is
// the correct, fast choice, and it keeps per-request verification cheap.
const (
	tokenPrefix = "gtk_"
	idBytes     = 4  // → 8 hex chars
	idHexLen    = 8  // len(hex(idBytes))
	secretBytes = 32 // 256 bits of CSPRNG entropy
)

// ErrMalformedToken means the string is not a well-formed gtk_ token (wrong
// prefix, wrong shape, or non-hex id). It is deliberately distinct from a
// well-formed-but-unknown/wrong token so the middleware can treat both as 401
// without leaking which failed.
var ErrMalformedToken = errors.New("malformed token")

// NewToken mints a fresh token: a random id, a random secret, the full bearer
// string to hand back once, and the SHA-256(secret) hash to store. The plaintext
// secret never leaves this function except in the returned string.
func NewToken() (id string, secretString string, hash []byte, err error) {
	idRaw := make([]byte, idBytes)
	if _, err = rand.Read(idRaw); err != nil {
		return "", "", nil, fmt.Errorf("auth: generate token id: %w", err)
	}
	secretRaw := make([]byte, secretBytes)
	if _, err = rand.Read(secretRaw); err != nil {
		return "", "", nil, fmt.Errorf("auth: generate token secret: %w", err)
	}
	id = hex.EncodeToString(idRaw)
	secret := base64.RawURLEncoding.EncodeToString(secretRaw)
	full := tokenPrefix + id + "_" + secret
	sum := sha256.Sum256([]byte(secret))
	return id, full, sum[:], nil
}

// parseToken splits a bearer string into its id and the SHA-256 of its secret,
// validating the shape. It returns ErrMalformedToken for anything that isn't a
// gtk_<8hex>_<secret> with a non-empty secret. Hashing here (rather than
// returning the raw secret) keeps the plaintext secret from lingering in caller
// memory.
func parseToken(s string) (id string, secretHash []byte, err error) {
	if !strings.HasPrefix(s, tokenPrefix) {
		return "", nil, ErrMalformedToken
	}
	body := s[len(tokenPrefix):]
	// Split into exactly id + "_" + secret. The secret's base64url alphabet has
	// no '_'-... actually RawURLEncoding uses '-' and '_', so SplitN on the FIRST
	// underscore separates the fixed-length id from the secret (which may itself
	// contain '_').
	idPart, secret, ok := strings.Cut(body, "_")
	if !ok {
		return "", nil, ErrMalformedToken
	}
	if len(idPart) != idHexLen || !isHex(idPart) {
		return "", nil, ErrMalformedToken
	}
	if secret == "" {
		return "", nil, ErrMalformedToken
	}
	sum := sha256.Sum256([]byte(secret))
	return idPart, sum[:], nil
}

// isHex reports whether s is all lowercase hex digits.
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// hashesEqual is a constant-time compare of two SHA-256 digests. Constant-time
// so a network attacker cannot recover the stored hash byte-by-byte via response
// timing.
func hashesEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
