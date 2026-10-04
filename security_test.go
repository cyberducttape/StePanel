package stepanel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecurityChecksDetectPrivateImportRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{ImportRoot: root, WebRoot: filepath.Join(root, "public"), Production: false}, Auth: Auth{Enabled: true}}
	checks := app.SecurityChecks()
	if checks[0].Status != "pass" {
		t.Fatalf("authentication check = %q, want pass", checks[0].Status)
	}
	foundImportCheck := false
	for _, check := range checks {
		if check.Name == "Import staging permissions" && check.Status != "pass" {
			t.Fatalf("import permissions check = %q, want pass", check.Status)
		}
		if check.Name == "Import staging permissions" {
			foundImportCheck = true
		}
	}
	if !foundImportCheck {
		t.Fatal("import permissions check is missing")
	}
}

func TestFormatUptime(t *testing.T) {
	if got := formatUptime(90061); got != "1d 01h" {
		t.Fatalf("formatUptime() = %q, want 1d 01h", got)
	}
}

func TestDatabaseAdminSecurityCheckRequiresVerifiedPolicy(t *testing.T) {
	check := databaseAdminSecurityCheck(DatabaseAdmin{AdminProduct: "phpMyAdmin", AdminInstalled: true})
	if check.Status != "warning" || check.Severity != "high" {
		t.Fatalf("database admin check = %#v", check)
	}
}

func TestWAFCapabilityIsWebserverSpecific(t *testing.T) {
	for _, test := range []struct{ webserver, status, provider string }{
		{"caddy", "unavailable", "external"},
		{"openlitespeed", "unavailable", "external"},
		{"apache", "available-not-enabled", "ModSecurity + OWASP CRS"},
	} {
		capability := wafCapability(test.webserver, map[string]string{})
		if capability.Status != test.status || capability.Provider != test.provider {
			t.Fatalf("%s capability = %#v", test.webserver, capability)
		}
	}
}

func TestApacheAccessPolicyIgnoresComments(t *testing.T) {
	hasIP, open := apacheAccessPolicy("# Require all granted\nRequire ip 127.0.0.1 ::1\n")
	if !hasIP || open {
		t.Fatalf("hasIP = %t, open = %t", hasIP, open)
	}
	_, open = apacheAccessPolicy("Require all granted\n")
	if !open {
		t.Fatal("open Apache policy was not detected")
	}
}
