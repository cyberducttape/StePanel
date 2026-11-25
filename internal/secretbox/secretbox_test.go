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

func TestPreviousKeysOpenButNeverSeal(t *testing.T) {
	oldKey, newKey := []byte("old-master-key-0123456789abcdef0123"), []byte("new-master-key-0123456789abcdef0123")
	oldBox, err := New(oldKey, "purpose")
	if err != nil {
		t.Fatal(err)
	}
	sealedOld, err := oldBox.Seal([]byte("secret"), "site", "NAME")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := NewWithPrevious(newKey, [][]byte{oldKey}, "purpose")
	if err != nil {
		t.Fatal(err)
	}
	plain, previous, err := rotated.OpenReportingPrevious(sealedOld, "site", "NAME")
	if err != nil || string(plain) != "secret" || !previous {
		t.Fatalf("open under previous key = %q, %v, %v", plain, previous, err)
	}
	if rotated.OpenedWithPrevious() != 1 {
		t.Fatalf("OpenedWithPrevious = %d, want 1", rotated.OpenedWithPrevious())
	}
	// Context binding still applies to previous keys.
	if _, _, err := rotated.OpenReportingPrevious(sealedOld, "site", "OTHER"); err == nil {
		t.Fatal("a previous-key value opened in another context")
	}
	resealed, err := rotated.Seal(plain, "site", "NAME")
	if err != nil {
		t.Fatal(err)
	}
	if _, previous, err := rotated.OpenReportingPrevious(resealed, "site", "NAME"); err != nil || previous {
		t.Fatalf("a re-sealed value must open with the current key: previous=%v err=%v", previous, err)
	}
	if _, err := oldBox.Open(resealed, "site", "NAME"); err == nil {
		t.Fatal("Seal used a previous key")
	}
	newOnly, err := New(newKey, "purpose")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newOnly.Open(sealedOld, "site", "NAME"); err == nil {
		t.Fatal("a value sealed under a retired key opened without it")
	}
	if got := newOnly.OpenedWithPrevious(); got != 0 {
		t.Fatalf("OpenedWithPrevious without previous keys = %d", got)
	}
}

func TestPreviousKeysAreValidated(t *testing.T) {
	key := []byte("current-master-key-0123456789abcdef")
	if _, err := NewWithPrevious(key, [][]byte{key}, "purpose"); err == nil {
		t.Fatal("a previous key equal to the current key was accepted")
	}
	if _, err := NewWithPrevious(key, [][]byte{nil}, "purpose"); err == nil {
		t.Fatal("an empty previous key was accepted")
	}
}
