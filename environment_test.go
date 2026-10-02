package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidEnvName(t *testing.T) {
	valid := []string{"PATH", "APP_ENV", "a", "_leading", "MixedCase123"}
	for _, name := range valid {
		if !validEnvName(name) {
			t.Errorf("validEnvName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "=EQUALS_FIRST", "123TOKEN", "9", "HAS SPACE", "HAS=EQUALS", "HAS/SLASH", strings.Repeat("A", 129)}
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

func TestMergeEnvironmentUpdatePreservesRedactedSecrets(t *testing.T) {
	current := map[string]environmentValue{
		"DB_PASSWORD": {Value: "secret123", Secret: true},
		"API_KEY":     {Value: "xyz987", Secret: true},
		"DEBUG":       {Value: "false"},
	}
	next, err := mergeEnvironmentUpdate(current, map[string]environmentUpdate{
		"DB_PASSWORD": {Operation: environmentOperationPreserve, Secret: true},
		"DEBUG":       {Value: "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]environmentValue{
		"DB_PASSWORD": {Value: "secret123", Secret: true},
		"API_KEY":     {Value: "xyz987", Secret: true},
		"DEBUG":       {Value: "true"},
	}
	if !reflect.DeepEqual(next, want) {
		t.Fatalf("merged = %#v; want %#v", next, want)
	}
	if current["DEBUG"].Value != "false" {
		t.Fatal("merge mutated the current environment map")
	}
}

func TestMergeEnvironmentUpdateRejectsImplicitBlankSecret(t *testing.T) {
	current := map[string]environmentValue{"DB_PASSWORD": {Value: "secret123", Secret: true}}
	if _, err := mergeEnvironmentUpdate(current, map[string]environmentUpdate{
		"DB_PASSWORD": {Value: "", Secret: true},
	}); err == nil {
		t.Fatal("blank secret without an operation was accepted; it must not imply deletion")
	}
}

func TestMergeEnvironmentUpdateOperations(t *testing.T) {
	current := map[string]environmentValue{"OLD": {Value: "x", Secret: true}, "KEEP": {Value: "k"}}
	next, err := mergeEnvironmentUpdate(current, map[string]environmentUpdate{
		"OLD":        {Operation: environmentOperationDelete},
		"STRIPE_KEY": {Operation: environmentOperationSet, Value: "sk_live", Secret: true},
		"EMPTY":      {Operation: environmentOperationSet, Value: "", Secret: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]environmentValue{
		"KEEP":       {Value: "k"},
		"STRIPE_KEY": {Value: "sk_live", Secret: true},
		"EMPTY":      {Value: "", Secret: true},
	}
	if !reflect.DeepEqual(next, want) {
		t.Fatalf("merged = %#v; want %#v", next, want)
	}

	for name, update := range map[string]environmentUpdate{
		"MISSING": {Operation: environmentOperationPreserve},
		"KEEP":    {Operation: "replace"},
		"BAD=":    {Value: "v"},
	} {
		if _, err := mergeEnvironmentUpdate(current, map[string]environmentUpdate{name: update}); err == nil {
			t.Errorf("update %s=%#v unexpectedly succeeded", name, update)
		}
	}
}

// TestBoundEnvironmentStoreKeepsPlaintextRuntimeState guards the boundary
// between the encrypted durable image and the plaintext runtime model: a bound
// persist must never leave ciphertext in memory or encrypt ciphertext again.
func TestBoundEnvironmentStoreKeepsPlaintextRuntimeState(t *testing.T) {
	dir := t.TempDir()
	db, err := openControlPlaneDB(filepath.Join(dir, "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := OpenEnvironmentStore(filepath.Join(dir, "environment.json"), "test-environment-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindControlPlaneState(store, db, "environment", store); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	store.values["demo"] = map[string]environmentValue{"DB_PASSWORD": {Value: "secret123", Secret: true}}
	err = store.persistLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := store.values["demo"]["DB_PASSWORD"].Value; got != "secret123" {
		t.Fatalf("runtime secret after first persist = %q; want plaintext", got)
	}

	// A second, unrelated persist must not re-encrypt the stored secret.
	store.mu.Lock()
	store.values["other"] = map[string]environmentValue{"DEBUG": {Value: "true"}}
	err = store.persistLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := store.values["demo"]["DB_PASSWORD"].Value; got != "secret123" {
		t.Fatalf("runtime secret after second persist = %q; want plaintext", got)
	}

	payload, found, err := readControlPlaneBlob(db, "environment")
	if err != nil || !found {
		t.Fatalf("read durable environment blob: found=%v err=%v", found, err)
	}
	if strings.Contains(string(payload), "secret123") {
		t.Fatal("durable environment blob contains a plaintext secret")
	}

	reloaded, err := OpenEnvironmentStore(filepath.Join(dir, "environment.json"), "test-environment-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindControlPlaneState(reloaded, db, "environment", reloaded); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.values["demo"]["DB_PASSWORD"].Value; got != "secret123" {
		t.Fatalf("secret after restart = %q; want single-decrypted plaintext", got)
	}
}

var systemdEnvironmentRoundTripValues = []string{
	"", "plain", "abc def", `abc"def`, `abc\def`, "abc#def", "$foo", "${HOME}", "'foo bar'",
	"x`whoami`y", `\"`, `trailing\`, "  padded  ", "tab\there", "%h%n", "=;&|<>*?~!",
	"sk_live_51H\"$\\`'#=", "ünïcödé ✓",
}

func TestEncodeSystemdEnvironmentValue(t *testing.T) {
	for value, want := range map[string]string{
		"":          `""`,
		"abc def":   `"abc def"`,
		`abc"def`:   `"abc\"def"`,
		`abc\def`:   `"abc\\def"`,
		"abc#def":   `"abc#def"`,
		"$foo":      `"\$foo"`,
		"'foo bar'": `"'foo bar'"`,
		"a`b":       "\"a\\`b\"",
	} {
		if got := encodeSystemdEnvironmentValue(value); got != want {
			t.Errorf("encodeSystemdEnvironmentValue(%q) = %s; want %s", value, got, want)
		}
	}
}

// TestSystemdEnvironmentFileRoundTrip feeds encoded values through a real
// systemd EnvironmentFile= and requires the process to see every value
// byte-for-byte. It needs a user systemd manager and is skipped without one.
func TestSystemdEnvironmentFileRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("systemd-run is not available")
	}
	var file bytes.Buffer
	for i, value := range systemdEnvironmentRoundTripValues {
		file.WriteString(fmt.Sprintf("STEPANEL_RT_%d=%s\n", i, encodeSystemdEnvironmentValue(value)))
	}
	path := filepath.Join(t.TempDir(), "site.env")
	if err := os.WriteFile(path, file.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "systemd-run", "--user", "--quiet", "--pipe", "--wait",
		"-p", "EnvironmentFile="+path, "/usr/bin/env", "-0").Output()
	if err != nil {
		t.Skipf("user systemd manager unavailable: %v", err)
	}
	got := map[string]string{}
	for _, entry := range bytes.Split(output, []byte{0}) {
		if name, value, ok := strings.Cut(string(entry), "="); ok && strings.HasPrefix(name, "STEPANEL_RT_") {
			got[name] = value
		}
	}
	for i, want := range systemdEnvironmentRoundTripValues {
		name := fmt.Sprintf("STEPANEL_RT_%d", i)
		if value, ok := got[name]; !ok || value != want {
			t.Errorf("%s = %q (present=%v); want %q", name, value, ok, want)
		}
	}
}
