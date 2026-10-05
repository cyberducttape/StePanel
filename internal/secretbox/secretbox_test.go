package secretbox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"testing"
)

func mustBox(t *testing.T, key, purpose string) *Box {
	t.Helper()
	box, err := New([]byte(key), purpose)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestSealOpensOnlyInItsContext(t *testing.T) {
	box := mustBox(t, "operator master key", "environment")
	sealed, err := box.Seal([]byte("db-password"), "site-a", "DB_PASSWORD")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Open(sealed, "site-a", "DB_PASSWORD")
	if err != nil || string(plain) != "db-password" {
		t.Fatalf("Open = %q, %v", plain, err)
	}
	for name, context := range map[string][]string{
		"other site":           {"site-b", "DB_PASSWORD"},
		"other variable":       {"site-a", "API_KEY"},
		"shifted field border": {"site-aD", "B_PASSWORD"},
		"missing field":        {"site-a"},
		"extra field":          {"site-a", "DB_PASSWORD", ""},
	} {
		if _, err := box.Open(sealed, context...); err == nil {
			t.Errorf("%s: ciphertext opened outside its context", name)
		}
	}
}

func TestPurposesAndKeysAreSeparated(t *testing.T) {
	env := mustBox(t, "operator master key", "environment")
	totp := mustBox(t, "operator master key", "account-totp")
	otherKey := mustBox(t, "another master key", "environment")
	sealed, err := env.Seal([]byte("secret"), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := totp.Open(sealed, "alice"); err == nil {
		t.Fatal("ciphertext opened under another purpose")
	}
	if _, err := otherKey.Open(sealed, "alice"); err == nil {
		t.Fatal("ciphertext opened under another master key")
	}
}

func TestOpenRejectsTamperingAndOtherFormats(t *testing.T) {
	box := mustBox(t, "k", "job-payload")
	sealed, err := box.Seal([]byte(`{"site":"a"}`), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := box.Open(tampered, "job-1"); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
	if _, err := box.Open(sealed[:len(Magic)+4], "job-1"); err == nil {
		t.Fatal("truncated ciphertext opened")
	}
	if _, err := box.Open([]byte("plain"), "job-1"); err != ErrNotSealed {
		t.Fatalf("plaintext Open error = %v, want ErrNotSealed", err)
	}
}

func TestOpenLegacyReadsPreviousFormat(t *testing.T) {
	master := []byte("operator master key")
	block, err := aes.NewCipher(LegacyKey(master))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	legacy := aead.Seal(append([]byte(nil), nonce...), nonce, []byte("old secret"), nil)
	plain, err := OpenLegacy(master, legacy)
	if err != nil || !bytes.Equal(plain, []byte("old secret")) {
		t.Fatalf("OpenLegacy = %q, %v", plain, err)
	}
	box := mustBox(t, string(master), "environment")
	if _, err := box.Open(legacy, "site"); err == nil {
		t.Fatal("legacy ciphertext accepted as context-bound")
	}
}
