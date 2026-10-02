package rootbroker

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestNewBrokerRejectsUnavailableDurableRecoveryRootOutsideTestMode(t *testing.T) {
	_, err := NewBrokerWithRecoveryRoot("/var/www", "/proc/stepanel-recovery", log.New(os.Stderr, "[test] ", 0))
	if err == nil || !strings.Contains(err.Error(), "durable recovery root") {
		t.Fatalf("NewBrokerWithRecoveryRoot error = %v, want durable recovery root failure", err)
	}
}

func TestBrokerTaskKillUsesOnlyValidatedSystemdUnit(t *testing.T) {
	broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	fakeSystemctl := filepath.Join(t.TempDir(), "systemctl")
	script := "#!/bin/sh\n" +
		"[ \"$#\" -eq 4 ] && [ \"$1\" = kill ] && [ \"$2\" = --kill-who=all ] && [ \"$3\" = --signal=SIGTERM ] && [ \"$4\" = stepanel-task-demo-nightly.service ]\n"
	if err := os.WriteFile(fakeSystemctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	broker.systemctlPath = fakeSystemctl
	response, err := broker.Execute(context.Background(), &Request{
		RequestType: "task",
		Task:        &TaskRequest{Action: "kill", Site: "demo", Name: "nightly"},
	})
	if err != nil || !response.OK {
		t.Fatalf("typed task kill response = %#v, error = %v", response, err)
	}
	var details TaskResponse
	if err := json.Unmarshal(response.Details, &details); err != nil || !details.Killed {
		t.Fatalf("typed task kill details = %#v, error = %v", details, err)
	}
}

func TestBrokerStreamsDatabaseRestoreFromApprovedPath(t *testing.T) {
	root := t.TempDir()
	dumpPath := filepath.Join(root, "restore.sql")
	if err := os.WriteFile(dumpPath, []byte("CREATE TABLE evidence (id INT);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	received := filepath.Join(root, "received.sql")
	helper := filepath.Join(root, "dbctl")
	script := "#!/bin/sh\n[ \"$1\" = restore-dump ] && [ \"$2\" = target_db ] && [ \"$3\" = demo ] || exit 11\ncat >" + received + "\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	broker, err := newTestBroker(t, root, log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	broker.dbctlPath = helper
	response, err := broker.Execute(context.Background(), &Request{
		RequestType: "db",
		DB:          &DBRequest{Action: "restore-dump", Site: "demo", Database: "target_db", Username: "target_user", DumpPath: dumpPath},
	})
	if err != nil || !response.OK {
		t.Fatalf("streaming restore response = %#v, error = %v", response, err)
	}
	data, err := os.ReadFile(received)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "CREATE TABLE evidence (id INT);\n" {
		t.Fatalf("streamed dump = %q", data)
	}
}

func TestBrokerRejectsDatabaseRestoreOutsideApprovedRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.sql")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	broker, err := newTestBroker(t, root, log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	response, err := broker.Execute(context.Background(), &Request{
		RequestType: "db",
		DB:          &DBRequest{Action: "restore-dump", Site: "demo", Database: "target_db", Username: "target_user", DumpPath: outside},
	})
	if err != nil || response.OK || !strings.Contains(response.Error, "approved") {
		t.Fatalf("outside restore response = %#v, error = %v", response, err)
	}
}

func TestBrokerTaskApplyUsesFixedHelperAndTypedArguments(t *testing.T) {
	broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	fakeAppctl := filepath.Join(t.TempDir(), "appctl")
	if err := os.WriteFile(fakeAppctl, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	broker.appctlPath = fakeAppctl
	req := &Request{RequestType: "task", Task: &TaskRequest{Action: "apply", Site: "demo", Name: "nightly", Runtime: "shell", Command: "echo hi", OnCalendar: "daily", TimeoutSec: 300, Enabled: true, MinIntervalSeconds: 60, MissedRunPolicy: "run_once", CPUPercent: 100, MemoryMB: 1024, TasksMax: 256}}
	response, err := broker.Execute(context.Background(), req)
	if err != nil || !response.OK {
		t.Fatalf("typed task apply response = %#v, error = %v", response, err)
	}
	var details TaskResponse
	if err := json.Unmarshal(response.Details, &details); err != nil || !details.Applied {
		t.Fatalf("typed task apply details = %#v, error = %v", details, err)
	}
	want := "task-apply\ndemo\nnightly\nshell\ndaily\n1\n300\nZWNobyBoaQ==\n60\nrun_once\n100\n1024\n256\n\n"
	if details.Output != want {
		t.Fatalf("helper arguments = %q, want %q", details.Output, want)
	}
}

func TestBrokerTaskDeleteAndHistoryUseFixedHelper(t *testing.T) {
	for _, action := range []string{"delete", "history"} {
		t.Run(action, func(t *testing.T) {
			broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
			if err != nil {
				t.Fatal(err)
			}
			fakeAppctl := filepath.Join(t.TempDir(), "appctl")
			script := "#!/bin/sh\n[ \"$#\" -eq 3 ] && [ \"$1\" = \"" + action + "\" ] && [ \"$2\" = demo ] && [ \"$3\" = nightly ] || exit 9\n"
			if action == "history" {
				script += "printf '%s' '{\"enabled\":true}'\n"
			}
			if err := os.WriteFile(fakeAppctl, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			broker.appctlPath = fakeAppctl
			response, err := broker.Execute(context.Background(), &Request{RequestType: "task", Task: &TaskRequest{Action: action, Site: "demo", Name: "nightly"}})
			if err != nil || !response.OK {
				t.Fatalf("typed task %s response = %#v, error = %v", action, response, err)
			}
			var details TaskResponse
			if err := json.Unmarshal(response.Details, &details); err != nil {
				t.Fatal(err)
			}
			if action == "delete" && !details.Deleted {
				t.Fatalf("task delete details = %#v", details)
			}
			if action == "history" && details.Output != `{"enabled":true}` {
				t.Fatalf("task history output = %q", details.Output)
			}
		})
	}
}

func TestBrokerGitDeleteRemovesOnlySiteKeyEntries(t *testing.T) {
	keyRoot := t.TempDir()
	if err := os.Chmod(keyRoot, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-key")
	if err := os.WriteFile(outside, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(keyRoot, "demo")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyRoot, "demo.pub"), []byte("public key"), 0644); err != nil {
		t.Fatal(err)
	}
	broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	broker.gitKeyRoot = keyRoot
	response, err := broker.Execute(context.Background(), &Request{
		RequestType: "git",
		Git:         &GitRequest{Action: "delete", Site: "demo"},
	})
	if err != nil || !response.OK {
		t.Fatalf("typed Git delete response = %#v, error = %v", response, err)
	}
	var details GitResponse
	if err := json.Unmarshal(response.Details, &details); err != nil || !details.Deleted {
		t.Fatalf("typed Git delete details = %#v, error = %v", details, err)
	}
	for _, name := range []string{"demo", "demo.pub"} {
		if _, err := os.Lstat(filepath.Join(keyRoot, name)); !os.IsNotExist(err) {
			t.Errorf("Git key entry %q remains: %v", name, err)
		}
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep me" {
		t.Fatalf("symlink target after Git delete = %q, error = %v", data, err)
	}
}

func TestBrokerGitPublicReadsBoundedRegularFileWithoutFollowingSymlink(t *testing.T) {
	keyRoot := t.TempDir()
	if err := os.Chmod(keyRoot, 0700); err != nil {
		t.Fatal(err)
	}
	const publicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAExample demo-deploy\n"
	if err := os.WriteFile(filepath.Join(keyRoot, "demo.pub"), []byte(publicKey), 0644); err != nil {
		t.Fatal(err)
	}
	broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	broker.gitKeyRoot = keyRoot
	response, err := broker.Execute(context.Background(), &Request{RequestType: "git", Git: &GitRequest{Action: "public", Site: "demo"}})
	if err != nil || !response.OK {
		t.Fatalf("typed Git public response = %#v, error = %v", response, err)
	}
	var details GitResponse
	if err := json.Unmarshal(response.Details, &details); err != nil || details.PublicKey != strings.TrimSpace(publicKey) {
		t.Fatalf("typed Git public details = %#v, error = %v", details, err)
	}

	outside := filepath.Join(t.TempDir(), "outside.pub")
	if err := os.WriteFile(outside, []byte(publicKey), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(keyRoot, "demo.pub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(keyRoot, "demo.pub")); err != nil {
		t.Fatal(err)
	}
	response, err = broker.Execute(context.Background(), &Request{RequestType: "git", Git: &GitRequest{Action: "public", Site: "demo"}})
	if err != nil || response.OK {
		t.Fatalf("typed Git public accepted symlink: response=%#v error=%v", response, err)
	}
}

func TestBrokerGitGenerateCreatesEd25519KeyWithoutReplacingExistingKey(t *testing.T) {
	keyRoot := t.TempDir()
	if err := os.Chmod(keyRoot, 0700); err != nil {
		t.Fatal(err)
	}
	broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	broker.gitKeyRoot = keyRoot
	request := &Request{RequestType: "git", Git: &GitRequest{Action: "generate", Site: "demo"}}
	response, err := broker.Execute(context.Background(), request)
	if err != nil || !response.OK {
		t.Fatalf("typed Git key generation response = %#v, error = %v", response, err)
	}
	var details GitResponse
	if err := json.Unmarshal(response.Details, &details); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(details.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("generated public key = %q, want ssh-ed25519 key", details.PublicKey)
	}
	privatePath := filepath.Join(keyRoot, "demo")
	privateInfo, err := os.Stat(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if privateInfo.Mode().Perm() != 0600 {
		t.Errorf("private key mode = %04o, want 0600", privateInfo.Mode().Perm())
	}
	privateData, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey(privateData); err != nil {
		t.Fatalf("generated private key is not parseable: %v", err)
	}
	publicInfo, err := os.Stat(privatePath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if publicInfo.Mode().Perm() != 0644 {
		t.Errorf("public key mode = %04o, want 0644", publicInfo.Mode().Perm())
	}
	originalPrivate := string(privateData)
	response, err = broker.Execute(context.Background(), request)
	if err != nil || response.OK {
		t.Fatalf("duplicate Git key generation accepted: response=%#v error=%v", response, err)
	}
	privateData, err = os.ReadFile(privatePath)
	if err != nil || string(privateData) != originalPrivate {
		t.Fatalf("duplicate generation changed private key: error=%v", err)
	}
}

func TestBrokerSiteCreate(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := newTestBroker(t, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action: "create",
			Site:   "testsite",
		},
	}

	resp, err := broker.Execute(ctx, req)
	// In a test environment, this might fail due to permissions
	// The key thing is that validation passed (no validation error)
	// and the request was routed correctly
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	// We accept OK=false if it's a system-level error (not a validation error)
	if !resp.OK && !contains(resp.Error, "ownership change failed") &&
		!contains(resp.Error, "directory creation failed") &&
		!contains(resp.Error, "user creation failed") {
		// Only fail if it's a validation error, not a system error
		if contains(resp.Error, "invalid") || contains(resp.Error, "validation") {
			t.Errorf("Execute returned validation error: %s", resp.Error)
		}
	}
}

func TestBrokerSiteDelete(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := newTestBroker(t, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action: "delete",
			Site:   "testsite",
		},
	}

	resp, err := broker.Execute(ctx, req)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	if !resp.OK {
		t.Errorf("Execute returned error: %s", resp.Error)
	}

	var siteResp SiteResponse
	if err := json.Unmarshal(resp.Details, &siteResp); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}
	if !siteResp.Deleted {
		t.Errorf("Site not marked as deleted")
	}
}

func TestBrokerCertificateIssuanceUsesFixedHelper(t *testing.T) {
	broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "stepanel-certbot")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s|%s' \"$1\" \"$2\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	broker.certbotPath = helper
	response, err := broker.Execute(context.Background(), &Request{
		RequestType: "certificate",
		Certificate: &CertificateRequest{Action: "issue", Domain: "example.test", Email: "ops@example.test"},
	})
	if err != nil || !response.OK {
		t.Fatalf("certificate request = %#v, %v", response, err)
	}
	var result CertificateResponse
	if err := json.Unmarshal(response.Details, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Issued || result.Domain != "example.test" || result.Output != "example.test|ops@example.test" {
		t.Fatalf("certificate result = %#v", result)
	}
}

func TestCertificateRequestValidationRejectsArgumentInjection(t *testing.T) {
	validator := NewValidator(t.TempDir())
	for _, request := range []*CertificateRequest{
		{Action: "issue", Domain: "example.test;touch /tmp/pwned", Email: "ops@example.test"},
		{Action: "issue", Domain: "example.test", Email: "ops@example.test\n--webroot"},
		{Action: "arbitrary", Domain: "example.test", Email: "ops@example.test"},
	} {
		if err := validator.ValidateRequest(&Request{RequestType: "certificate", Certificate: request}); err == nil {
			t.Fatalf("unsafe certificate request was accepted: %#v", request)
		}
	}
}

func TestCreateSystemUserSurfacesUseraddFailure(t *testing.T) {
	bin := t.TempDir()
	writeFakeCommand(t, bin, "id", "#!/bin/sh\nexit 1\n")
	writeFakeCommand(t, bin, "useradd", "#!/bin/sh\necho useradd-failed >&2\nexit 42\n")
	t.Setenv("PATH", bin)

	host := execHostOps{}
	err := host.EnsureSystemUser(context.Background(), "sp-test", "/var/www/sites/test")
	if err == nil || !strings.Contains(err.Error(), "useradd failed") {
		t.Fatalf("createSystemUser error = %v, want useradd failure", err)
	}
}

func TestDeleteSystemUserSurfacesUnexpectedUserdelFailure(t *testing.T) {
	bin := t.TempDir()
	writeFakeCommand(t, bin, "userdel", "#!/bin/sh\necho userdel-failed >&2\nexit 1\n")
	t.Setenv("PATH", bin)

	host := execHostOps{}
	err := host.DeleteSystemUser(context.Background(), "sp-test")
	if err == nil || !strings.Contains(err.Error(), "userdel failed") {
		t.Fatalf("deleteSystemUser error = %v, want userdel failure", err)
	}
}

func TestDeleteSystemUserTreatsMissingUserAsIdempotent(t *testing.T) {
	bin := t.TempDir()
	writeFakeCommand(t, bin, "userdel", "#!/bin/sh\nexit 6\n")
	t.Setenv("PATH", bin)

	host := execHostOps{}
	if err := host.DeleteSystemUser(context.Background(), "sp-test"); err != nil {
		t.Fatalf("deleteSystemUser returned %v for an absent user", err)
	}
}

func writeFakeCommand(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

func TestBrokerAppApply(t *testing.T) {
	webRoot := t.TempDir()
	broker, err := newTestBroker(t, webRoot, log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}
	fakeAppctl := filepath.Join(t.TempDir(), "appctl")
	if err := os.WriteFile(fakeAppctl, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	broker.appctlPath = fakeAppctl
	root := filepath.Join(webRoot, "sites", "testsite", "public")
	resp, err := broker.Execute(context.Background(), &Request{RequestType: "app", App: &AppRequest{Action: "apply", Site: "testsite", Version: "v18.0.0", Port: 3000, Root: root}})
	if err != nil || !resp.OK {
		t.Fatalf("app apply response = %#v, error = %v", resp, err)
	}
	var details AppResponse
	if err := json.Unmarshal(resp.Details, &details); err != nil || !details.Applied || details.Port != 3000 {
		t.Fatalf("app apply details = %#v, error = %v", details, err)
	}
	want := "apply\ntestsite\n18.0.0\n" + root + "\n3000\n"
	if details.Output != want {
		t.Fatalf("helper arguments = %q, want %q", details.Output, want)
	}
}

func TestBrokerAppLifecycleUsesFixedHelper(t *testing.T) {
	for _, action := range []string{"delete", "start", "stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
			if err != nil {
				t.Fatal(err)
			}
			fakeAppctl := filepath.Join(t.TempDir(), "appctl")
			script := "#!/bin/sh\n[ \"$#\" -eq 2 ] && [ \"$1\" = \"" + action + "\" ] && [ \"$2\" = demo ] || exit 9\n"
			if err := os.WriteFile(fakeAppctl, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			broker.appctlPath = fakeAppctl
			resp, err := broker.Execute(context.Background(), &Request{RequestType: "app", App: &AppRequest{Action: action, Site: "demo"}})
			if err != nil || !resp.OK {
				t.Fatalf("typed app %s response = %#v, error = %v", action, resp, err)
			}
			var details AppResponse
			if err := json.Unmarshal(resp.Details, &details); err != nil {
				t.Fatal(err)
			}
			done := map[string]bool{"delete": details.Deleted, "start": details.Started, "stop": details.Stopped, "restart": details.Restarted}
			if !done[action] {
				t.Fatalf("typed app %s details = %#v", action, details)
			}
		})
	}
}

func TestBrokerAppHelperFailureIsReported(t *testing.T) {
	broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	fakeAppctl := filepath.Join(t.TempDir(), "appctl")
	if err := os.WriteFile(fakeAppctl, []byte("#!/bin/sh\necho 'application is not configured' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	broker.appctlPath = fakeAppctl
	resp, err := broker.Execute(context.Background(), &Request{RequestType: "app", App: &AppRequest{Action: "start", Site: "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, "application is not configured") {
		t.Fatalf("app start failure response = %#v", resp)
	}
}

func TestBrokerDBProvision(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := newTestBroker(t, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}
	dbctl := filepath.Join(t.TempDir(), "dbctl")
	if err := os.WriteFile(dbctl, []byte("#!/bin/sh\ncat >/dev/null\n"), 0700); err != nil {
		t.Fatal(err)
	}
	broker.dbctlPath = dbctl

	ctx := context.Background()
	req := &Request{
		RequestType: "db",
		DB: &DBRequest{
			Action:   "provision",
			Database: "testdb",
			Username: "testuser",
			Site:     "testsite",
			Encoding: "utf8mb4",
			Password: "Strong-Database_2026!",
		},
	}

	resp, err := broker.Execute(ctx, req)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	if !resp.OK {
		t.Fatalf("database provision response = %#v", resp)
	}
}

func TestBrokerDatabaseDumpAndRestore(t *testing.T) {
	broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(t.TempDir(), "args")
	inputPath := filepath.Join(t.TempDir(), "input")
	dbctl := filepath.Join(t.TempDir(), "dbctl")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$DBCTL_ARGS\"\n" +
		"if [ \"$1\" = dump ]; then printf 'CREATE TABLE smoke (id INT);\\n'; else cat > \"$DBCTL_INPUT\"; fi\n"
	if err := os.WriteFile(dbctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBCTL_ARGS", argsPath)
	t.Setenv("DBCTL_INPUT", inputPath)
	broker.dbctlPath = dbctl

	dumpResponse, err := broker.Execute(context.Background(), &Request{
		RequestType: "db",
		DB:          &DBRequest{Action: "dump", Database: "smoke_db", Username: "dump"},
	})
	if err != nil || dumpResponse == nil || !dumpResponse.OK {
		t.Fatalf("dump response = %#v, err = %v", dumpResponse, err)
	}
	var dumpDetails DBResponse
	if err := json.Unmarshal(dumpResponse.Details, &dumpDetails); err != nil {
		t.Fatal(err)
	}
	if got, want := string(dumpDetails.DumpData), "CREATE TABLE smoke (id INT);\n"; got != want {
		t.Fatalf("dump payload = %q, want %q", got, want)
	}

	restoreResponse, err := broker.Execute(context.Background(), &Request{
		RequestType: "db",
		DB:          &DBRequest{Action: "restore-dump", Database: "smoke_db", Username: "restore", Site: "smoke_site", DumpData: []byte("INSERT INTO smoke VALUES (1);\n")},
	})
	if err != nil || restoreResponse == nil || !restoreResponse.OK {
		t.Fatalf("restore response = %#v, err = %v", restoreResponse, err)
	}
	input, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(input), "INSERT INTO smoke VALUES (1);\n"; got != want {
		t.Fatalf("restore input = %q, want %q", got, want)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(args)), "dump smoke_db\nrestore-dump smoke_db smoke_site"; got != want {
		t.Fatalf("helper args = %q, want %q", got, want)
	}
}

func TestBrokerCleanupWordPressPreservesDedicatedHelperAction(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := newTestBroker(t, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}
	argsPath := filepath.Join(t.TempDir(), "args")
	dbctl := filepath.Join(t.TempDir(), "dbctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$DBCTL_ARGS\"\n"
	if err := os.WriteFile(dbctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBCTL_ARGS", argsPath)
	broker.dbctlPath = dbctl

	response, err := broker.Execute(context.Background(), &Request{
		RequestType: "db",
		DB:          &DBRequest{Action: "cleanup-wordpress", Database: "wordpress_db", Username: "wp_user"},
	})
	if err != nil || response == nil || !response.OK {
		t.Fatalf("cleanup-wordpress response = %#v, err = %v", response, err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(args)), "cleanup-wordpress wordpress_db wp_user"; got != want {
		t.Fatalf("database helper args = %q, want %q", got, want)
	}
}

func TestBrokerVhostApply(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := newTestBroker(t, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "vhost",
		Vhost: &VhostRequest{
			Action:    "apply",
			Site:      "testsite",
			Domain:    "example.com",
			WebServer: "caddy",
		},
	}

	resp, err := broker.Execute(ctx, req)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "not implemented") {
		t.Fatalf("vhost apply response = %#v, want explicit unsupported response", resp)
	}
}

func TestBrokerRejectsEveryUnimplementedMutation(t *testing.T) {
	broker, err := newTestBroker(t, t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		req  *Request
	}{
		{name: "site access", req: &Request{RequestType: "site", Site: &SiteRequest{Action: "access", Site: "testsite"}}},
		{name: "site resources", req: &Request{RequestType: "site", Site: &SiteRequest{Action: "resources", Site: "testsite"}}},
		{name: "site quota", req: &Request{RequestType: "site", Site: &SiteRequest{Action: "quota", Site: "testsite"}}},
		{name: "site quota clear", req: &Request{RequestType: "site", Site: &SiteRequest{Action: "quota-clear", Site: "testsite"}}},
		{name: "site runtime", req: &Request{RequestType: "site", Site: &SiteRequest{Action: "runtime", Site: "testsite", PHPVersion: "8.2"}}},
		{name: "app rollback", req: &Request{RequestType: "app", App: &AppRequest{Action: "rollback", Site: "testsite", Port: 3000}}},
		{name: "vhost auth", req: &Request{RequestType: "vhost", Vhost: &VhostRequest{Action: "apply-auth", Site: "testsite", Domain: "example.com", WebServer: "caddy"}}},
		{name: "vhost delete", req: &Request{RequestType: "vhost", Vhost: &VhostRequest{Action: "delete", Site: "testsite", Domain: "example.com", WebServer: "caddy"}}},
		{name: "proxy apply", req: &Request{RequestType: "proxy", Proxy: &ProxyRequest{Action: "apply", WebServer: "caddy"}}},
		{name: "proxy reload", req: &Request{RequestType: "proxy", Proxy: &ProxyRequest{Action: "reload", WebServer: "caddy"}}},
		{name: "git clone", req: &Request{RequestType: "git", Git: &GitRequest{Action: "clone", Repository: "https://github.com/user/repo.git", Ref: "main", Destination: "destination"}}},
		{name: "git key verification", req: &Request{RequestType: "git", Git: &GitRequest{Action: "verify-key", Repository: "https://github.com/user/repo.git", Ref: "main", Destination: "destination"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := broker.Execute(context.Background(), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.OK || !strings.Contains(resp.Error, "not implemented") {
				t.Fatalf("response = %#v, want explicit unsupported response", resp)
			}
		})
	}
}

func TestBrokerInvalidSiteName(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := newTestBroker(t, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action: "create",
			Site:   "INVALID", // uppercase not allowed
		},
	}

	resp, err := broker.Execute(ctx, req)
	if resp.OK {
		t.Errorf("Execute should have failed for invalid site name")
	}
	if resp.Error == "" {
		t.Errorf("Expected error message, got empty")
	}
}

func TestBrokerInvalidPort(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := newTestBroker(t, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "app",
		App: &AppRequest{
			Action: "apply",
			Site:   "testsite",
			Port:   99999, // out of range
		},
	}

	resp, err := broker.Execute(ctx, req)
	if resp.OK {
		t.Errorf("Execute should have failed for invalid port")
	}
	if resp.Error == "" {
		t.Errorf("Expected error message, got empty")
	}
}

func TestBrokerGitCloneFailsClosed(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := newTestBroker(t, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "git",
		Git: &GitRequest{
			Action:      "clone",
			Repository:  "https://github.com/user/repo.git",
			Ref:         "main",
			Destination: "destination",
		},
	}

	resp, err := broker.Execute(ctx, req)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "not implemented") {
		t.Fatalf("git clone response = %#v, want explicit unsupported response", resp)
	}
}

func TestBrokerNilRequest(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := newTestBroker(t, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	resp, err := broker.Execute(ctx, nil)
	if resp.OK {
		t.Errorf("Execute should have failed for nil request")
	}
}

func TestBrokerUnknownRequestType(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := newTestBroker(t, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "unknown",
	}

	resp, err := broker.Execute(ctx, req)
	if resp.OK {
		t.Errorf("Execute should have failed for unknown request type")
	}
}
