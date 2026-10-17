package stepanel

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

func TestAutoSuspendAccountFailureInjectionPreservesState(t *testing.T) {
	root := t.TempDir()
	store, err := OpenAccountStore(filepath.Join(root, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Admission keeps usage within the plan, so model a plan whose site limit
	// was lowered after the account filled it.
	if _, err := store.Create("customer", "a sufficiently long customer password", testTOTPSecret, "professional", []string{"site-one", "site-two"}); err != nil {
		t.Fatal(err)
	}
	professional := hostingPlans["professional"]
	t.Cleanup(func() { hostingPlans["professional"] = professional })
	lowered := professional
	lowered.SiteLimit = 1
	hostingPlans["professional"] = lowered
	t.Setenv("STEPANEL_FAIL_AT", "suspend:before-persist")
	app := &App{Accounts: store}
	if err := app.checkPlanLimits(); err == nil || !strings.Contains(err.Error(), "failure injection") {
		t.Fatalf("checkPlanLimits error = %v, want injected failure", err)
	}
	account, ok := store.Get("customer")
	if !ok || account.Suspended {
		t.Fatalf("automatic suspension changed state after injected failure: %#v, exists=%v", account, ok)
	}
}

func TestPlanLimitCheckDoesNotSuspendAccountsAtTheirLimit(t *testing.T) {
	root := t.TempDir()
	store, err := OpenAccountStore(filepath.Join(root, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("full", "a sufficiently long customer password", testTOTPSecret, "starter", []string{"site-one"}); err != nil {
		t.Fatal(err)
	}
	app := &App{Accounts: store, Config: Config{AuditLog: filepath.Join(root, "audit.jsonl")}}
	if err := app.checkPlanLimits(); err != nil {
		t.Fatal(err)
	}
	if account, ok := store.Get("full"); !ok || account.Suspended {
		t.Fatalf("account exactly at its plan limit was suspended: %#v, exists=%v", account, ok)
	}
}
