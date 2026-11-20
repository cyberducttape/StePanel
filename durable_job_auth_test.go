package stepanel

import "testing"

func TestAuthorizeDurableSiteJobRechecksSuspensionAndOwnership(t *testing.T) {
	app := &App{Auth: Auth{Username: "admin"}, Accounts: &AccountStore{accounts: map[string]HostingAccount{
		"customer": {Username: "customer", Plan: "starter", Sites: []string{"owned"}},
	}}}
	_, err := app.authorizeDurableSiteJob("owned", "customer", false)
	if err != nil {
		t.Fatalf("owned customer job rejected: %v", err)
	}
	app.Accounts.accounts["customer"] = HostingAccount{Username: "customer", Plan: "starter", Sites: []string{"owned"}, Suspended: true}
	_, err = app.authorizeDurableSiteJob("owned", "customer", false)
	if err == nil {
		t.Fatal("suspended customer job was accepted")
	}
	_, err = app.authorizeDurableSiteJob("unowned", "admin", false)
	if err != nil {
		t.Fatalf("administrator job rejected: %v", err)
	}
	_, err = app.authorizeDurableSiteJob("owned", "scheduler", true)
	if err != nil {
		t.Fatalf("scheduled job rejected: %v", err)
	}
}

func TestAuthorizeDurableSiteJobStaysBoundToQueuedTenant(t *testing.T) {
	accounts := &AccountStore{accounts: map[string]HostingAccount{
		"tenant-a": {Username: "tenant-a", Plan: "professional", Sites: []string{"shop"}},
		"member":   {Username: "member", TenantID: "tenant-a", Role: "member", Plan: "professional"},
		"tenant-b": {Username: "tenant-b", Plan: "professional"},
	}}
	app := &App{Auth: Auth{Username: "admin"}, Accounts: accounts}

	access, err := app.authorizeDurableSiteJobForTenant("shop", "member", "tenant-a", true, false)
	if err != nil {
		t.Fatalf("queued tenant job rejected before reassignment: %v", err)
	}
	if access.TenantID() != "tenant-a" {
		t.Fatalf("durable capability tenant = %q, want tenant-a", access.TenantID())
	}

	// Transfer both the member and site. A fresh ownership check alone would
	// authorize the old job under tenant-b; the captured tenant must still match.
	accounts.accounts["tenant-a"] = HostingAccount{Username: "tenant-a", Plan: "professional"}
	accounts.accounts["tenant-b"] = HostingAccount{Username: "tenant-b", Plan: "professional", Sites: []string{"shop"}}
	accounts.accounts["member"] = HostingAccount{Username: "member", TenantID: "tenant-b", Role: "member", Plan: "professional"}
	if _, err := app.authorizeDurableSiteJobForTenant("shop", "member", "tenant-a", true, false); err == nil {
		t.Fatal("durable job was re-authorized after its site and actor moved to another tenant")
	}
	if _, err := app.authorizeDurableSiteJobForTenant("shop", "member", "tenant-b", true, false); err != nil {
		t.Fatalf("newly queued tenant-b job rejected: %v", err)
	}
}
