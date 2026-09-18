package main

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
