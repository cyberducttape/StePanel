package stepanel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitDeployRejectsUnassignedCustomerSite(t *testing.T) {
	a := &App{}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"customer": {Username: "customer", Plan: "starter", Sites: []string{"owned"}},
	}}
	a.Auth = Auth{Username: "admin"}
	r := httptest.NewRequest(http.MethodPost, "/api/sites/git-deploy", nil)
	r = r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "customer"))
	if a.canAccessSite(r, "other") {
		t.Fatal("unassigned site was authorized")
	}
	if !a.canAccessSite(r, "owned") {
		t.Fatal("assigned site was rejected")
	}
}
