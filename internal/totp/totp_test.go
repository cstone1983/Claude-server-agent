package totp

import (
	"encoding/base32"
	"testing"
	"time"
)

func TestRFC6238(t *testing.T) {
	// RFC 6238 test vector (SHA1, key "12345678901234567890"), last 6 digits.
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	got, _ := Code(secret, time.Unix(59, 0))
	if got != "287082" {
		t.Fatalf("got %s", got)
	}
	if !Verify(secret, "287082", time.Unix(59, 0)) || Verify(secret, "287083", time.Unix(59, 0)) {
		t.Fatal("verify")
	}
}
