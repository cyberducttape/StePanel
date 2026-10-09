package stepanel

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFTPSMutationUsesClassAAudit(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	stdinFile := filepath.Join(dir, "stdin")
	helper := filepath.Join(dir, "sitectl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + argsFile + "\ncat >> " + stdinFile + "\n[ \"$STEPANEL_TEST_FAIL\" != 1 ]\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	auditLog := filepath.Join(dir, "audit.jsonl")
	app := &App{Config: Config{SiteCtl: helper, AuditLog: auditLog}}
	post := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/ftp", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		app.ftpEndpoint(response, request)
		return response
	}

	if response := post(`{"site":"shop","enabled":true,"password":"ftps-password-0123"}`); response.Code != http.StatusOK {
		t.Fatalf("enable status = %d %s", response.Code, response.Body.String())
	}
	if response := post(`{"site":"shop","enabled":false,"password":"ignored-on-disable"}`); response.Code != http.StatusOK {
		t.Fatalf("disable status = %d %s", response.Code, response.Body.String())
	}
	t.Setenv("STEPANEL_TEST_FAIL", "1")
	if response := post(`{"site":"shop","enabled":true,"password":"ftps-password-0123"}`); response.Code != http.StatusBadGateway {
		t.Fatalf("failed enable status = %d %s", response.Code, response.Body.String())
	}

	want := "site.ftps.enabled.initiated,site.ftps.enabled,site.ftps.disabled,site.ftps.enabled.initiated,site.ftps.enabled.failed"
	if got := strings.Join(auditActions(t, auditLog), ","); got != want {
		t.Fatalf("ledger = %s, want %s", got, want)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(args); got != "ftp shop 1\nftp shop 0\nftp shop 1\n" {
		t.Fatalf("helper args = %q", got)
	}
	// The password reaches the helper on stdin only, and never on disable.
	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(stdin); got != "ftps-password-0123ftps-password-0123" {
		t.Fatalf("helper stdin = %q", got)
	}
}

func TestFTPSEnableRefusedWithoutAuditLedger(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	helper := filepath.Join(dir, "sitectl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{SiteCtl: helper, AuditLog: unavailableAuditLog(t)}}
	request := httptest.NewRequest(http.MethodPost, "/api/ftp", strings.NewReader(`{"site":"shop","enabled":true,"password":"ftps-password-0123"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ftpEndpoint(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d %s, want 503", response.Code, response.Body.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("FTPS password was installed without a recorded intent")
	}
}
