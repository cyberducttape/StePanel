package stepanel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueuedNodeDeploymentStopsWhenQueuingTokenIsRevoked(t *testing.T) {
	app, _ := newSiteOperationFixture(t, http.StatusOK, `{}`)
	accounts := &AccountStore{accounts: map[string]HostingAccount{
		"customer": {Username: "customer", Role: "owner", Sites: []string{"shop"}},
	}}
	app.Accounts = accounts
	app.Auth.Accounts = accounts
	allowed := map[string]bool{"site:deploy": true}
	token, _, err := app.APITokens.createScoped("customer", "deployment", nil, []string{"site:deploy"}, allowed)
	if err != nil {
		t.Fatal(err)
	}

	request := tokenRequest(http.MethodPost, "/api/node/deploy", `{"site":"shop","domain":"shop.example.com","node_version":"20.1.0","port":3000,"backend":"http://127.0.0.1:3001","stage":"completed"}`, "customer", "site:deploy")
	request = request.WithContext(context.WithValue(request.Context(), apiTokenIDKey{}, token.ID))
	response := httptest.NewRecorder()
	app.startNodeDeployment(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("queue status = %d, body %s", response.Code, response.Body.String())
	}
	if err := app.APITokens.revoke("customer", token.ID); err != nil {
		t.Fatal(err)
	}
	item, claimed, err := app.Jobs.ClaimNext("node-worker", "node.deployment")
	if err != nil || !claimed {
		t.Fatalf("claim Node deployment: claimed=%v err=%v", claimed, err)
	}
	_, err = app.handleNodeDeploymentJob(context.Background(), item)
	if err == nil || !strings.Contains(err.Error(), "revoked or expired") {
		t.Fatalf("worker error = %v, want revoked-token refusal", err)
	}
}

func TestNodeDeploymentRecoveryRestoresSnapshotAtEveryStepCheckpoint(t *testing.T) {
	stages := []string{
		"prepared",
		"select-node-applying", "select-node-applied",
		"deploy-app-applying", "deploy-app-applied",
		"deploy-proxy-applying", "deploy-proxy-applied",
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			app, item, input, root := newNodeDeploymentRecoveryFixture(t, stage)
			snapshotManifest := string(input.Snapshot.Manifest)
			snapshotProxy := string(input.Snapshot.Proxy)
			proxyPath := input.Snapshot.ProxyPath
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // Simulate shutdown/cancellation after the checkpointed step.
			if err := app.recoverNodeDeploymentCheckpoint(ctx, item, &input); err != nil {
				t.Fatalf("recover checkpoint: %v", err)
			}
			assertFileContents(t, filepath.Join(root, "web", "sites", "shop", ".nvmrc"), "v18.19.0\n")
			assertFileContents(t, filepath.Join(root, "apps", "shop.json"), snapshotManifest)
			assertFileContents(t, filepath.Join(root, "apps", "shop.json.bak"), "previous backup manifest\n")
			assertFileContents(t, proxyPath, snapshotProxy)
			if input.Stage != "" || input.Snapshot != nil {
				t.Fatalf("checkpoint not cleared after recovery: stage=%q snapshot=%v", input.Stage, input.Snapshot != nil)
			}
		})
	}
}

func TestNodeDeploymentRollbackRefusesAfterJobLeaseLoss(t *testing.T) {
	app, item, input, root := newNodeDeploymentRecoveryFixture(t, "deploy-proxy-applying")
	if _, err := app.Jobs.db.Exec(`UPDATE jobs SET lease_expires_at=0 WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	err := app.rollbackNodeDeployment(context.Background(), item, input, *input.Snapshot)
	if err == nil || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("rollback error = %v, want lost lease refusal", err)
	}
	assertFileContents(t, filepath.Join(root, "web", "sites", "shop", ".nvmrc"), "partially deployed\n")
}

func newNodeDeploymentRecoveryFixture(t *testing.T, stage string) (*App, Job, nodeDeploymentRequest, string) {
	t.Helper()
	app, _ := newSiteOperationFixture(t, http.StatusOK, `{}`)
	root := t.TempDir()
	webRoot := filepath.Join(root, "web")
	siteRoot := filepath.Join(webRoot, "sites", "shop")
	appRoot := filepath.Join(root, "apps")
	proxyRoot := filepath.Join(root, "proxy")
	for _, dir := range []string{filepath.Join(siteRoot, "public"), appRoot, proxyRoot} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
	}
	app.Config.WebRoot = webRoot
	app.Config.AppRoot = appRoot
	app.Config.ProxyRoot = proxyRoot
	app.Config.AppCtl = writeNodeTestHelper(t, root, "appctl")
	app.Config.ProxyCtl = writeNodeTestHelper(t, root, "proxyctl")
	app.Config.WebServer = "caddy"

	manifest := AppManifest{Site: "shop", Domain: "shop.example.com", Version: "18.19.0", Port: 3000, Root: filepath.Join(siteRoot, "public"), State: "running"}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	proxyPath, err := safePath(proxyRoot, proxyConfigName("caddy", "shop", "shop.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := nodeDeploymentSnapshot{
		NVMRC: []byte("v18.19.0\n"), Manifest: manifestBytes,
		ManifestBak: []byte("previous backup manifest\n"),
		Proxy:       []byte("shop.example.com {\n\treverse_proxy 127.0.0.1:3000\n}\n"),
		HadNVMRC:    true, HadManifest: true, HadManifestBak: true, HadProxy: true,
		ProxyPath: proxyPath,
	}
	proxyHelper := "#!/bin/sh\nif [ \"$1\" = apply ]; then cat > " + shellQuote(proxyPath) + " <<'EOF'\n" + string(snapshot.Proxy) + "EOF\nfi\n"
	if err := os.WriteFile(app.Config.ProxyCtl, []byte(proxyHelper), 0700); err != nil {
		t.Fatal(err)
	}
	input := nodeDeploymentRequest{Site: "shop", Domain: "shop.example.com", Version: "20.1.0", Port: 3100, Backend: "http://127.0.0.1:3101", Actor: "admin", Stage: stage, Snapshot: &snapshot}
	for path, contents := range map[string]string{
		filepath.Join(siteRoot, ".nvmrc"):       "partially deployed\n",
		filepath.Join(appRoot, "shop.json"):     `{"site":"shop","version":"20.1.0"}`,
		filepath.Join(appRoot, "shop.json.bak"): "partial backup manifest\n",
		proxyPath:                               "partial proxy configuration\n",
	} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Jobs.Enqueue("node.deployment", "shop", "", payload, 3); err != nil {
		t.Fatal(err)
	}
	item, claimed, err := app.Jobs.ClaimNext("node-worker", "node.deployment")
	if err != nil || !claimed {
		t.Fatalf("claim recovery job: claimed=%v err=%v", claimed, err)
	}
	return app, item, input, root
}

func writeNodeTestHelper(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
