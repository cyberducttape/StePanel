package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSiteDeployUsesValidatedVHostHelper(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "account", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(root, "args")
	helper := filepath.Join(root, "vhostctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$TEST_VHOST_ARGS\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_VHOST_ARGS", argsPath)
	app := &App{Config: Config{WebRoot: webRoot, VHostRoot: filepath.Join(root, "vhosts"), VHostCtl: helper}}
	req := httptest.NewRequest(http.MethodPost, "/api/sites/deploy", bytes.NewBufferString(`{"site":"account","domain":"WWW.Example.com"}`))
	response := httptest.NewRecorder()
	app.siteDeploy(response, req)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertTestFile(t, argsPath, "apply account www.example.com\n")
}

func TestSiteDeployRejectsMissingDocumentRoot(t *testing.T) {
	app := &App{Config: Config{WebRoot: t.TempDir(), VHostCtl: "/not-run"}}
	req := httptest.NewRequest(http.MethodPost, "/api/sites/deploy", bytes.NewBufferString(`{"site":"missing","domain":"example.com"}`))
	response := httptest.NewRecorder()
	app.siteDeploy(response, req)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "document root") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestSiteVHostConfigNameMatchesWebserverRouteConvention(t *testing.T) {
	if got := siteVHostConfigName("caddy", "my-site", "WWW.Example.com"); got != "site-my-site-www_example_com.caddy" {
		t.Fatalf("Caddy route name = %q", got)
	}
	if got := siteVHostConfigName("apache", "my-site", "WWW.Example.com"); got != "site-my-site-www_example_com.conf" {
		t.Fatalf("Apache route name = %q", got)
	}
}

func TestSiteListFiltersUnmanagedFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "site-account-example_com.conf"), "managed")
	writeTestFile(t, filepath.Join(root, "site-caddy-caddy_example_com.caddy"), "managed")
	writeTestFile(t, filepath.Join(root, "unmanaged.conf"), "unmanaged")
	app := &App{Config: Config{VHostRoot: root}}
	response := httptest.NewRecorder()
	app.siteList(response, httptest.NewRequest(http.MethodGet, "/api/sites", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "site-account-example_com.conf") || !strings.Contains(response.Body.String(), "site-caddy-caddy_example_com.caddy") || strings.Contains(response.Body.String(), "unmanaged.conf") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestSiteOverviewGroupsManagedResources(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "account", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	vhostRoot := filepath.Join(root, "vhosts")
	writeTestFile(t, filepath.Join(vhostRoot, "site-account-example_com.conf"), "managed")
	writeTestFile(t, filepath.Join(vhostRoot, "site-account-caddy_example_com.caddy"), "managed")
	appRoot := filepath.Join(root, "apps")
	writeTestFile(t, filepath.Join(appRoot, "account.json"), `{"site":"account","domain":"app.example.com","node_version":"v20.1.0","port":3000,"state":"running"}`)
	app := &App{Config: Config{WebRoot: webRoot, VHostRoot: vhostRoot, AppRoot: appRoot}}
	response := httptest.NewRecorder()
	app.siteOverviewList(response, httptest.NewRequest(http.MethodGet, "/api/sites/overview", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"site":"account"`) || !strings.Contains(response.Body.String(), "example.com") || !strings.Contains(response.Body.String(), "caddy.example.com") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestSiteOverviewReportsSiteInventoryReadFailure(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	if err := os.MkdirAll(webRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "sites"), []byte("not-a-directory"), 0600); err != nil {
		t.Fatal(err)
	}

	app := &App{Config: Config{WebRoot: webRoot}}
	response := httptest.NewRecorder()
	app.siteOverviewList(response, httptest.NewRequest(http.MethodGet, "/api/sites/overview", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s; want inventory read failure", response.Code, response.Body.String())
	}
}

func TestSiteOverviewReportsManagedRouteReadFailure(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "account"), 0750); err != nil {
		t.Fatal(err)
	}
	vhostRoot := filepath.Join(root, "vhosts")
	if err := os.WriteFile(vhostRoot, []byte("not-a-directory"), 0600); err != nil {
		t.Fatal(err)
	}

	app := &App{Config: Config{WebRoot: webRoot, VHostRoot: vhostRoot}}
	response := httptest.NewRecorder()
	app.siteOverviewList(response, httptest.NewRequest(http.MethodGet, "/api/sites/overview", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s; want route read failure", response.Code, response.Body.String())
	}
}

func TestSiteOverviewResourceIncludesHyphenatedSiteResources(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "my-site", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	vhostRoot := filepath.Join(root, "vhosts")
	proxyRoot := filepath.Join(root, "proxies")
	appRoot := filepath.Join(root, "apps")
	writeTestFile(t, filepath.Join(vhostRoot, "site-my-site-www_example_com.conf"), "managed")
	writeTestFile(t, filepath.Join(proxyRoot, "my-site-api_example_com.conf"), "managed")
	writeTestFile(t, filepath.Join(appRoot, "my-site.json"), `{"site":"my-site","domain":"api.example.com","node_version":"v20.1.0","port":3000,"state":"running"}`)
	app := &App{Config: Config{WebRoot: webRoot, VHostRoot: vhostRoot, ProxyRoot: proxyRoot, AppRoot: appRoot}}
	response := httptest.NewRecorder()
	app.siteOverviewResource(response, httptest.NewRequest(http.MethodGet, "/api/sites/overview/my-site", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "www.example.com") || !strings.Contains(body, "api_example_com") || !strings.Contains(body, `"applications":[{`) {
		t.Fatalf("status = %d, body = %s", response.Code, body)
	}
}
