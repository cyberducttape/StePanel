package stepanel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatabaseResourceDeniesCrossTenantDatabase(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "dbctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf 'bob_db\\tbob-site\\tbob_user\\t4096\\tutf8mb4\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{DBEngine: "mysql", DBCtl: helper}, Auth: Auth{Username: "admin"}}
	app.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"alice-site"}},
		"bob":   {Username: "bob", Plan: "starter", Sites: []string{"bob-site"}},
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/databases/bob_db", nil)
	req = req.WithContext(context.WithValue(req.Context(), apiTokenUsernameKey{}, "alice"))
	response := httptest.NewRecorder()
	app.databaseResource(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant database access = %d %s, want 403", response.Code, response.Body.String())
	}
}

func TestMySQLCompatibleDatabaseModes(t *testing.T) {
	for engine, want := range map[string]bool{"": true, "mysql": true, "mariadb": true, "postgresql": false} {
		if got := mysqlCompatible(Config{DBEngine: engine}); got != want {
			t.Fatalf("mysqlCompatible(%q) = %t, want %t", engine, got, want)
		}
	}
}

func TestValidDatabasePassword(t *testing.T) {
	if !validDatabasePassword("Long-Datab4se-Passw0rd") {
		t.Fatal("valid generated-style password was rejected")
	}
	for _, password := range []string{"short", "twenty characters'bad", "twenty characters bad"} {
		if validDatabasePassword(password) {
			t.Fatalf("unsafe password %q was accepted", password)
		}
	}
}

func TestManagedDatabaseInventory(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "dbctl")
	script := "#!/bin/sh\nprintf 'site_db\\tsite\\tsite_user\\t4096\\tutf8mb4\\n'\n"
	if err := os.WriteFile(helper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	items, err := managedDatabaseInventory(Config{DBCtl: helper})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "site_db" || items[0].Bytes != 4096 || items[0].User != "site_user" {
		t.Fatalf("unexpected inventory: %#v", items)
	}
}

func TestLabDirectRootBrokerRequiresExplicitLabSignal(t *testing.T) {
	t.Setenv("STEPANEL_LAB_DIRECT_ROOT_BROKER", "1")
	t.Setenv("STEPANEL_SKIP_STARTUP_HOST_RECONCILE", "1")
	if !labDirectRootBrokerEnabled() {
		t.Fatal("explicit lab broker mode was not recognized")
	}

	t.Setenv("STEPANEL_LAB_DIRECT_ROOT_BROKER", "0")
	t.Setenv("STEPANEL_KILL_AT", "terminate:site-state")
	if !labDirectRootBrokerEnabled() {
		t.Fatal("explicit termination lab signal was not recognized")
	}
}

func TestDatabaseResourceReturnsReadOnlyDetailWithoutCSRF(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "dbctl")
	script := "#!/bin/sh\nprintf 'site_db\\tsite\\tsite_user\\t4096\\tutf8mb4\\n'\n"
	if err := os.WriteFile(helper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{DBEngine: "mysql", DBCtl: helper}}
	response := httptest.NewRecorder()
	app.databaseResource(response, httptest.NewRequest(http.MethodGet, "/api/databases/site_db", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"site_db"`) || strings.Contains(response.Body.String(), "password") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestDatabaseProvisionDoesNotReturnPassword(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "dbctl")
	logPath := filepath.Join(root, "calls")
	script := "#!/bin/sh\nread password\nprintf '%s|%s\\n' \"$*\" \"$password\" > \"$TEST_DB_LOG\"\n"
	if err := os.WriteFile(helper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_DB_LOG", logPath)
	if err := os.MkdirAll(filepath.Join(root, "sites", "site", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{DBEngine: "mysql", DBCtl: helper, WebRoot: root}, Auth: Auth{Username: "admin"}}
	body := `{"name":"site_db","user":"site_user","site":"site","password":"Long-Datab4se-Passw0rd","encoding":"utf8mb4"}`
	response := httptest.NewRecorder()
	app.databaseCollection(response, httptest.NewRequest(http.MethodPost, "/api/databases", strings.NewReader(body)))
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "Passw0rd") {
		t.Fatalf("unexpected provision response: %d %s", response.Code, response.Body.String())
	}
	call, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(call), "provision site_db site_user site utf8mb4|Long-Datab4se-Passw0rd") {
		t.Fatalf("helper did not receive expected protected input: %q, %v", call, err)
	}
}

func TestDatabaseDeleteRequiresExactConfirmation(t *testing.T) {
	app := &App{Config: Config{DBCtl: "/does/not/run"}, Auth: Auth{}}
	response := httptest.NewRecorder()
	body := `{"user":"site_user","confirm":"DROP wrong"}`
	app.databaseResource(response, httptest.NewRequest(http.MethodDelete, "/api/databases/site_db", strings.NewReader(body)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", response.Code, response.Body.String())
	}
}

func TestDatabaseHostDescribesLocalConnections(t *testing.T) {
	for _, host := range []string{"", "localhost", "127.0.0.1"} {
		if got := databaseHost(Config{DBHost: host}); got != "local socket" {
			t.Fatalf("databaseHost(%q) = %q", host, got)
		}
	}
	if got := databaseHost(Config{DBHost: "db.internal"}); got != "db.internal" {
		t.Fatalf("remote database host = %q", got)
	}
}
