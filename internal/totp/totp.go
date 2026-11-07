// Package totp implements RFC 6238 time-based one-time passwords
// (HMAC-SHA1, 6 digits, 30 s step), compatible with common authenticator apps.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random 160-bit base32 secret.
func NewSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b32.EncodeToString(b)
}

// URI returns an otpauth:// URI for QR codes.
func URI(secret, issuer, account string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

func code(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}

// Code returns the current code (for tests).
func Code(secret string, t time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	return code(key, uint64(t.Unix()/30)), nil
}

// Verify checks a code, allowing one step of clock skew either way.
func Verify(secret, input string, now time.Time) bool {
	input = strings.ReplaceAll(strings.TrimSpace(input), " ", "")
	if len(input) != 6 {
		return false
	}
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return false
	}
	c := uint64(now.Unix() / 30)
	ok := 0
	for _, d := range []int64{-1, 0, 1} {
		ok |= subtle.ConstantTimeCompare([]byte(code(key, uint64(int64(c)+d))), []byte(input))
	}
	return ok == 1
}
