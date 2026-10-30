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

func TestPythonDeploymentDurableOperationUsesTypedService(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	app, _ := newSiteOperationFixture(t, http.StatusInternalServerError, "HTTP replay must not run")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sites", "shop", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(root, "python.args")
	helper := filepath.Join(root, "appctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+shellQuote(argsPath)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	app.Config.WebRoot, app.Config.AppRoot, app.Config.AppCtl = root, filepath.Join(root, "apps"), helper
	body := []byte(`{"site":"shop","version":"v3.13","entrypoint":"app:app","port":8000,"workers":2,"root":"/attacker/path","state":"running"}`)
	if err := siteOperationRoutes["python.deploy"].validate(body); err != nil {
		t.Fatalf("valid typed Python command rejected: %v", err)
	}
	if err := siteOperationRoutes["python.deploy"].validate([]byte(`{"site":"shop","version":"3.13","entrypoint":"app:app","port":8000,"workers":2,"unexpected":true}`)); err == nil {
		t.Fatal("unknown Python command field was accepted")
	}
	payload, err := json.Marshal(siteOperationPayload{
		Operation: "python.deploy", Path: "/api/python/deploy", Site: "shop", Actor: "admin", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := app.Jobs.Enqueue(siteOperationKind, "shop", "", payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.handleSiteOperationJob(context.Background(), claimSiteOperation(t, app))
	if err != nil {
		t.Fatalf("typed Python deployment failed: %v", err)
	}
	var deployed PythonApp
	if err := json.Unmarshal(result, &deployed); err != nil || deployed.Site != "shop" || deployed.Root != filepath.Join(root, "sites", "shop", "public") || deployed.State != "running" {
		t.Fatalf("Python deployment result = %+v (%v)", deployed, err)
	}
	if got, err := os.ReadFile(argsPath); err != nil || !strings.Contains(string(got), "python-apply\nshop\n3.13\n") {
		t.Fatalf("typed executor did not invoke helper correctly: %q (%v)", got, err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "apps", "shop-python.json"))
	if err != nil || !strings.Contains(string(manifest), `"state": "running"`) || strings.Contains(string(manifest), "/attacker/path") {
		t.Fatalf("server-owned Python manifest incorrect: %s (%v)", manifest, err)
	}
	if got, ok := app.Jobs.Get(job.ID); !ok || got.State == "queued" {
		t.Fatalf("durable job did not leave queued state: %+v, found=%v", got, ok)
	}
}
