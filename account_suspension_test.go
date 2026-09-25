package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetAccountSuspendedFailureInjectionPreservesState(t *testing.T) {
	root := t.TempDir()
	store, err := OpenAccountStore(filepath.Join(root, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEPANEL_FAIL_AT", "suspend:before-persist")
	app := &App{Accounts: store}
	if _, err := app.setAccountSuspended(context.Background(), "customer", true); err == nil || !strings.Contains(err.Error(), "failure injection") {
		t.Fatalf("suspension error = %v, want injected failure", err)
	}
	account, ok := store.Get("customer")
	if !ok || account.Suspended {
		t.Fatalf("account state after injected failure = %#v, exists=%v", account, ok)
	}
	if _, err := os.Stat(filepath.Join(root, "accounts.json")); err != nil {
		t.Fatalf("account state was not persisted: %v", err)
	}
}
