package auth

import (
	"testing"
	"time"
)

func TestOTPGenerationAndHashing(t *testing.T) {
	code, err := NewOTP()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 {
		t.Fatalf("OTP must be 6 digits, got %q", code)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			t.Fatalf("OTP contains non-digit: %q", code)
		}
	}
	pepper := []byte("test-pepper-secret")
	h1 := HashCode(code, pepper)
	h2 := HashCode(code, pepper)
	if h1 != h2 {
		t.Fatal("HMAC must be deterministic")
	}
	if h1 == code {
		t.Fatal("code stored in plaintext")
	}
	if HashCode(code+"9", pepper) == h1 {
		t.Fatal("different code produced same hash")
	}
}

func TestJWTLifecycle(t *testing.T) {
	secret := []byte("jwt-secret-test-12345678901234567890")
	tok, err := MakeToken(42, "node@dmesh.network", true, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseToken(tok, secret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.AccountID != 42 || claims.Email != "node@dmesh.network" || !claims.Admin {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	if _, err := ParseToken(tok, []byte("wrong-secret")); err == nil {
		t.Fatal("token validated with wrong secret")
	}
}

func TestAPIKeyHashing(t *testing.T) {
	key := "dmesh_abc123"
	h := HashToken(key)
	if h == key || len(h) != 64 {
		t.Fatal("API key hash must be SHA-256 hex, never plaintext")
	}
	if HashToken(key) != h {
		t.Fatal("hashing not deterministic")
	}
}
