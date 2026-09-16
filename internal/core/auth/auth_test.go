package auth

import (
	"testing"
)

func TestAuth_HashAndCheck(t *testing.T) {
	pw := "secret_password"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if !CheckPasswordHash(pw, hash) {
		t.Errorf("CheckPasswordHash failed for correct password")
	}

	if CheckPasswordHash("wrong_password", hash) {
		t.Errorf("CheckPasswordHash succeeded for wrong password")
	}
}

func TestAuth_SessionTokens(t *testing.T) {
	secret := "test_secret_key_123"
	login := "admin"

	token := CreateSessionToken(login, secret)
	extractedLogin, valid := ValidateSessionToken(token, secret)
	if !valid || extractedLogin != login {
		t.Fatalf("expected valid token with login %q, got %q, valid: %v", login, extractedLogin, valid)
	}

	// Tampered token
	_, validTampered := ValidateSessionToken(token+"tampered", secret)
	if validTampered {
		t.Errorf("tampered token should be invalid")
	}

	// Different secret
	_, validDiffSecret := ValidateSessionToken(token, "different_secret")
	if validDiffSecret {
		t.Errorf("token with different secret should be invalid")
	}
}
