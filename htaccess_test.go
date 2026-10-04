package stepanel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const wordpressHTAccess = `<IfModule mod_rewrite.c>
RewriteEngine On
RewriteBase /
RewriteRule ^index\.php$ - [L]
RewriteCond %{REQUEST_FILENAME} !-f
RewriteCond %{REQUEST_FILENAME} !-d
RewriteRule . /index.php [L]
</IfModule>`

func TestTranslateHTAccessWordPressAndRedirect(t *testing.T) {
	conversion, err := translateHTAccess(wordpressHTAccess + "\nRedirect permanent /old https://example.test/new\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(conversion.Warnings) != 0 || !strings.Contains(conversion.CaddyDirectives, "try_files {path}") || !strings.Contains(conversion.CaddyDirectives, "redir /old https://example.test/new 301") {
		t.Fatalf("conversion = %#v", conversion)
	}
}

func TestTranslateHTAccessReportsUnsupportedDirectives(t *testing.T) {
	conversion, err := translateHTAccess("AllowOverride All\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(conversion.Warnings) != 1 || conversion.Supported != 0 {
		t.Fatalf("conversion = %#v", conversion)
	}
}

func TestTranslateHTAccessRejectsAmbiguousRedirectTargets(t *testing.T) {
	for _, target := range []string{"//evil.example/path", "/safe#caddy-comment", "https://example.test/path#fragment"} {
		conversion, err := translateHTAccess("Redirect /old " + target + "\n")
		if err != nil {
			t.Fatal(err)
		}
		if len(conversion.Warnings) != 1 || conversion.CaddyDirectives != "" {
			t.Fatalf("target %q conversion = %#v", target, conversion)
		}
	}
}

func TestHTAccessApplyFailsClosedOnWarnings(t *testing.T) {
	app := &App{Config: Config{WebServer: "caddy"}, Auth: Auth{}}
	request := httptest.NewRequest(http.MethodPost, "/api/caddy/htaccess", strings.NewReader(`{"site":"demo","domain":"demo.example.test","content":"Deny from all","action":"apply"}`))
	response := httptest.NewRecorder()
	app.htaccessMigration(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "unsupported directive") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHTAccessApplyUsesCaddyHelper(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "demo", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "helper-input")
	argsPath := filepath.Join(root, "helper-args")
	helper := filepath.Join(root, "vhostctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$TEST_ARGS\"\ncat > \"$TEST_INPUT\"\n"
	if err := os.WriteFile(helper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_ARGS", argsPath)
	t.Setenv("TEST_INPUT", inputPath)
	app := &App{Config: Config{WebServer: "caddy", WebRoot: webRoot, VHostCtl: helper}, Auth: Auth{}}
	body, err := json.Marshal(htaccessRequest{Site: "demo", Domain: "demo.example.test", Content: wordpressHTAccess, Action: "apply"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/caddy/htaccess", bytes.NewReader(body))
	response := httptest.NewRecorder()
	app.htaccessMigration(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertTestFile(t, argsPath, "import-htaccess demo demo.example.test\n")
	data, err := os.ReadFile(inputPath)
	if err != nil || !strings.Contains(string(data), "try_files {path}") {
		t.Fatalf("helper input = %q, error = %v", data, err)
	}
}
