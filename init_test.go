package main

import (
	"testing"
)

func TestGenerateSecret(t *testing.T) {
	secret, err := generateSecret(32)
	if err != nil {
		t.Fatalf("generateSecret failed: %v", err)
	}
	if len(secret) == 0 {
		t.Fatal("generated secret is empty")
	}
	// Base64-encoded 32 bytes should be 44 characters
	if len(secret) != 44 {
		t.Fatalf("unexpected secret length: got %d, want 44", len(secret))
	}
}

func TestGenerateSecretIsDifferent(t *testing.T) {
	secret1, err1 := generateSecret(32)
	if err1 != nil {
		t.Fatalf("first generateSecret failed: %v", err1)
	}
	secret2, err2 := generateSecret(32)
	if err2 != nil {
		t.Fatalf("second generateSecret failed: %v", err2)
	}
	if secret1 == secret2 {
		t.Fatal("two generated secrets should not be equal")
	}
}

