package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidEnvName(t *testing.T) {
	valid := []string{"PATH", "APP_ENV", "a", "_leading", "MixedCase123"}
	for _, name := range valid {
		if !validEnvName(name) {
			t.Errorf("validEnvName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "=EQUALS_FIRST", "HAS SPACE", "HAS=EQUALS", "HAS/SLASH", strings.Repeat("A", 129)}
	for _, name := range invalid {
		if validEnvName(name) {
			t.Errorf("validEnvName(%q) = true, want false", name)
		}
	}
}

func TestEnvironmentStoreRequiresKeyForSecretState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.json")
	store, err := OpenEnvironmentStore(path, "test-environment-key")
	if err != nil {
		t.Fatal(err)
	}
	store.values["demo"] = map[string]environmentValue{
		"APP_KEY": {Value: "secret", Secret: true},
	}
	store.mu.Lock()
	err = store.persistLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEnvironmentStore(path, ""); err == nil || !strings.Contains(err.Error(), "encryption key") {
		t.Fatalf("OpenEnvironmentStore without key error = %v; want encryption-key error", err)
	}
}

func TestEnvironmentStoreEncryptWithoutKeyReturnsError(t *testing.T) {
	store := &EnvironmentStore{}
	if _, err := store.encrypt("secret"); err == nil {
		t.Fatal("encrypt without a key unexpectedly succeeded")
	}
}
