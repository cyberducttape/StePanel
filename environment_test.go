package stepanel

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// TestEnvironmentSecretLifecycleSurvivesEditsRestartAndReconcile walks the
// dangerous operator workflow end to end: edits through the HTTP API, an
// unrelated site's change, a full control-plane restart, and reconciliation
// into the host helper. The secret must survive every step until it is
// deleted explicitly.
func TestEnvironmentSecretLifecycleSurvivesEditsRestartAndReconcile(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "web")
	for _, site := range []string{"site-a", "site-b"} {
		if err := os.MkdirAll(filepath.Join(webRoot, "sites", site, "public"), 0750); err != nil {
			t.Fatal(err)
		}
	}
	hostEnv := filepath.Join(root, "host-env")
	if err := os.MkdirAll(hostEnv, 0700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "stepanel-appctl")
	script := "#!/bin/sh\n[ \"$1\" = env-apply ] || exit 2\ncat > '" + hostEnv + "'/\"$2\".env\n"
	if err := os.WriteFile(helper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "control-plane.db")
	statePath := filepath.Join(root, "environment.json")
	const key = "test-environment-key-with-32-characters"

	start := func() *App {
		t.Helper()
		db, err := openControlPlaneDB(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store, err := OpenEnvironmentStore(statePath, key)
		if err != nil {
			t.Fatal(err)
		}
		found, err := bindControlPlaneState(store, db, "environment", store)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			store.mu.Lock()
			err = store.persistLocked()
			store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
		}
		return &App{Config: Config{WebRoot: webRoot, AppCtl: helper}, Auth: Auth{Username: "admin"}, Environments: store}
	}
	put := func(a *App, site, body string, wantStatus int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPut, "/api/sites/environment/"+site, strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "admin"))
		w := httptest.NewRecorder()
		a.siteEnvironment(w, r)
		if w.Code != wantStatus {
			t.Fatalf("PUT %s %s = %d %s; want %d", site, body, w.Code, w.Body.String(), wantStatus)
		}
	}
	runtimeSecret := func(a *App) (string, bool) {
		a.Environments.mu.RLock()
		defer a.Environments.mu.RUnlock()
		value, ok := a.Environments.values["site-a"]["DB_PASSWORD"]
		return value.Value, ok
	}
	hostFile := func(site string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(hostEnv, site+".env"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	requireSecret := func(a *App, want, step string) {
		t.Helper()
		if got, ok := runtimeSecret(a); !ok || got != want {
			t.Fatalf("%s: runtime DB_PASSWORD = %q (present=%v); want %q", step, got, ok, want)
		}
		if line := `DB_PASSWORD="` + want + `"`; !strings.Contains(hostFile("site-a"), line) {
			t.Fatalf("%s: host environment %q lacks %s", step, hostFile("site-a"), line)
		}
	}

	a := start()
	put(a, "site-a", `{"DB_PASSWORD":{"operation":"set","value":"alpha","secret":true}}`, 204)
	requireSecret(a, "alpha", "create secret")

	put(a, "site-a", `{"DB_PASSWORD":{"operation":"preserve","secret":true},"DEBUG":{"value":"true"}}`, 204)
	requireSecret(a, "alpha", "change unrelated variable")
	if !strings.Contains(hostFile("site-a"), `DEBUG="true"`) {
		t.Fatalf("DEBUG was not applied: %q", hostFile("site-a"))
	}

	put(a, "site-b", `{"OTHER":{"value":"x","secret":true}}`, 204)
	requireSecret(a, "alpha", "change another site")

	// Restart the control plane and reconcile from durable state only.
	if err := os.Remove(filepath.Join(hostEnv, "site-a.env")); err != nil {
		t.Fatal(err)
	}
	a = start()
	if _, failed := a.reconcileEnvironments(context.Background()); len(failed) != 0 {
		t.Fatalf("reconcile failed: %v", failed)
	}
	requireSecret(a, "alpha", "restart and reconcile")

	put(a, "site-a", `{"DB_PASSWORD":{"operation":"set","value":"beta","secret":true}}`, 204)
	requireSecret(a, "beta", "rotate secret")

	put(a, "site-a", `{"DB_PASSWORD":{"value":"","secret":true}}`, 422)
	requireSecret(a, "beta", "blank secret without operation")

	put(a, "site-a", `{"DB_PASSWORD":{"operation":"delete"}}`, 204)
	if _, ok := runtimeSecret(a); ok {
		t.Fatal("explicit delete left DB_PASSWORD in desired state")
	}
	if strings.Contains(hostFile("site-a"), "DB_PASSWORD") {
		t.Fatalf("explicit delete left DB_PASSWORD on the host: %q", hostFile("site-a"))
	}
	if !strings.Contains(hostFile("site-a"), `DEBUG="true"`) {
		t.Fatalf("explicit delete removed unrelated variables: %q", hostFile("site-a"))
	}
}

// FuzzEnvironmentPersistenceBoundary requires that any value survives the
// encrypted durable image and a reload unchanged, alongside an existing secret.
func FuzzEnvironmentPersistenceBoundary(f *testing.F) {
	for _, value := range systemdEnvironmentRoundTripValues {
		f.Add(value, true)
	}
	f.Add("U3pD3KuXkcWj+52e+dqpg5Cq", true)
	f.Fuzz(func(t *testing.T, value string, secret bool) {
		if !utf8.ValidString(value) {
			t.Skip("the API accepts only valid UTF-8 values")
		}
		// Fuzz subtest names contain '#', which SQLite would read as a URI
		// fragment, so use a plainly named directory.
		dir, err := os.MkdirTemp("", "stepanel-env-fuzz")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		db, err := openControlPlaneDB(filepath.Join(dir, "control-plane.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		store, err := OpenEnvironmentStore(filepath.Join(dir, "environment.json"), "fuzz-environment-key")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bindControlPlaneState(store, db, "environment", store); err != nil {
			t.Fatal(err)
		}
		want := map[string]environmentValue{"VALUE": {Value: value, Secret: secret}, "ANCHOR": {Value: "anchor", Secret: true}}
		for i := 0; i < 2; i++ {
			store.mu.Lock()
			store.values["demo"] = cloneEnvironmentValues(want)
			err = store.persistLocked()
			store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(store.values["demo"], want) {
				t.Fatalf("runtime after persist %d = %#v; want %#v", i, store.values["demo"], want)
			}
		}
		reloaded, err := OpenEnvironmentStore(filepath.Join(dir, "environment.json"), "fuzz-environment-key")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bindControlPlaneState(reloaded, db, "environment", reloaded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(reloaded.values["demo"], want) {
			t.Fatalf("reloaded = %#v; want %#v", reloaded.values["demo"], want)
		}
	})
}
