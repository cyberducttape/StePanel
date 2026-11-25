package accounts

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const testTOTP = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"

func validAccount(t *testing.T) HostingAccount {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("a sufficiently long password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return HostingAccount{Username: "alice", TenantID: "alice", PasswordHash: string(hash), TOTPSecret: testTOTP,
		Plan: "starter", Sites: []string{"shop"}, CreatedAt: time.Now()}
}

func TestValidateAcceptsAWellFormedOwner(t *testing.T) {
	if err := Validate(validAccount(t), true); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsInvalidRecords(t *testing.T) {
	cases := map[string]func(*HostingAccount){
		"bad username":          func(a *HostingAccount) { a.Username = "Alice!" },
		"bad tenant":            func(a *HostingAccount) { a.TenantID = "" },
		"unknown role":          func(a *HostingAccount) { a.Role = "root" },
		"owner of other tenant": func(a *HostingAccount) { a.TenantID = "bob" },
		"member owning sites":   func(a *HostingAccount) { a.Role, a.TenantID = "developer", "bob" },
		"not bcrypt":            func(a *HostingAccount) { a.PasswordHash = "plain" },
		"short TOTP":            func(a *HostingAccount) { a.TOTPSecret = "JBSWY3DP" },
		"unknown plan":          func(a *HostingAccount) { a.Plan = "platinum" },
		"over the site limit":   func(a *HostingAccount) { a.Sites = []string{"shop", "blog"} },
		"duplicate site":        func(a *HostingAccount) { a.Plan, a.Sites = "agency", []string{"shop", "shop"} },
		"missing creation":      func(a *HostingAccount) { a.CreatedAt = time.Time{} },
	}
	for name, mutate := range cases {
		account := validAccount(t)
		mutate(&account)
		if err := Validate(account, true); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPublicSummaryOmitsCredentials(t *testing.T) {
	account := validAccount(t)
	account.RecoveryCodeHashes = []string{"hash"}
	summary := Public(account)
	if !summary.MFAEnabled || summary.Role != "owner" || summary.TenantID != "alice" {
		t.Fatalf("summary = %+v", summary)
	}
	summary.Sites[0] = "changed"
	if account.Sites[0] != "shop" {
		t.Fatal("the summary shares the account's site slice")
	}
}

func TestTenantRoleAndNameRules(t *testing.T) {
	if TenantID(HostingAccount{Username: "bob"}) != "bob" || TenantID(HostingAccount{Username: "dev", TenantID: "bob"}) != "bob" {
		t.Fatal("tenant ID defaults to the username only for owners")
	}
	if Role(HostingAccount{Role: " Viewer "}) != "viewer" || Role(HostingAccount{}) != "owner" {
		t.Fatal("role is normalized and defaults to owner")
	}
	for value, want := range map[string]string{"site_1-a": "site_1-a", " shop ": "shop", "Shop": "", "": "", strings.Repeat("a", 33): ""} {
		if got := SafeName(value); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", value, got, want)
		}
	}
	if _, err := DecodeTOTPSecret("jbswy3dp ehpk3pxp jbswy3dp ehpk3pxp"); err != nil {
		t.Fatalf("lowercase, spaced secret: %v", err)
	}
}
