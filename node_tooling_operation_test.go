package stepanel

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeToolingDurableOperationUsesTypedService(t *testing.T) {
	app, _ := newSiteOperationFixture(t, http.StatusInternalServerError, `should not dispatch through HTTP`)
	root := t.TempDir()
	public := filepath.Join(root, "sites", "shop", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(root, "node-tool.args")
	helper := filepath.Join(root, "appctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argsPath) + "\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	app.Config.WebRoot = root
	app.Config.AppCtl = helper
	payloadBody := []byte(`{"site":"shop","action":"build","package_manager":"npm"}`)
	if err := siteOperationRoutes["node.tooling"].validate(payloadBody); err != nil {
		t.Fatalf("valid typed command rejected: %v", err)
	}
	if err := siteOperationRoutes["node.tooling"].validate([]byte(`{"site":"shop","action":"build","package_manager":"npm","other":"ignored"}`)); err == nil {
		t.Fatal("unknown command field was accepted")
	}
	payload, err := json.Marshal(siteOperationPayload{
		Operation: "node.tooling", Path: "/api/node/tooling", Site: "shop", Actor: "admin", Body: payloadBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := app.Jobs.Enqueue(siteOperationKind, "shop", "", payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	item := claimSiteOperation(t, app)
	output, err := app.handleSiteOperationJob(context.Background(), item)
	if err != nil {
		t.Fatalf("typed Node tooling job failed: %v", err)
	}
	var result nodeToolingCommand
	if err := json.Unmarshal(output, &result); err != nil || result.Site != "shop" || result.Action != "build" || result.PackageManager != "npm" {
		t.Fatalf("typed operation result = %s (%v)", output, err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal("typed executor did not run the configured helper:", err)
	}
	if !strings.Contains(string(args), "node-tool\nshop\nbuild\nnpm\n") {
		t.Fatalf("helper received unexpected arguments: %q", args)
	}
	if got, ok := app.Jobs.Get(job.ID); !ok || got.State == "queued" {
		t.Fatalf("durable job did not leave queued state: %+v, found=%v", got, ok)
	}
}

func TestDurableSiteOperationScopeRechecksCurrentIdentity(t *testing.T) {
	app := &App{Auth: Auth{Username: "admin"}}
	if !app.durableSiteOperationScopeAllowed(siteOperationPrincipal{Actor: "admin", Token: true}, "site:deploy") {
		t.Fatal("administrator was denied")
	}
	if app.durableSiteOperationScopeAllowed(siteOperationPrincipal{Actor: "customer", Token: true, Scopes: []string{"site:read"}}, "site:deploy") {
		t.Fatal("customer without account state or deploy scope was authorized")
	}
	app.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"customer": {Username: "customer", Role: "developer", Sites: []string{"shop"}},
	}}
	if !app.durableSiteOperationScopeAllowed(siteOperationPrincipal{Actor: "customer", Token: true, Scopes: []string{"deploy:write"}}, "site:deploy") {
		t.Fatal("current developer role and deploy:write scope were not accepted")
	}
	if app.durableSiteOperationScopeAllowed(siteOperationPrincipal{Actor: "customer", Token: true, Scopes: []string{"site:read"}}, "site:deploy") {
		t.Fatal("read-only token was authorized for deployment")
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
