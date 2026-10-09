package siteidentity

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var sites = []string{"a", "demo", "my_site", "abcdefghijklmnopqrstuvwxyz012345", "under_score_name_that_is_long", "x-y_z"}

func TestUnixUserIsValidAccountName(t *testing.T) {
	pattern := regexp.MustCompile(`^sp-[a-z0-9-]{1,18}-[0-9a-f]{8}$`)
	for _, site := range sites {
		user := UnixUser(site)
		if !pattern.MatchString(user) || len(user) > 32 {
			t.Errorf("UnixUser(%q) = %q is not a valid bounded account name", site, user)
		}
	}
}

// TestUnixUserMatchesShellHelper runs the derivation lines extracted from the
// installed shell helper so the two implementations cannot drift silently.
func TestUnixUserMatchesShellHelper(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum is unavailable")
	}
	helper, err := os.ReadFile(filepath.Join("..", "..", "deploy", "integrations", "stepanel-sitectl"))
	if err != nil {
		t.Fatal(err)
	}
	var derivation []string
	for _, line := range strings.Split(string(helper), "\n") {
		if strings.HasPrefix(line, "hash=") || strings.HasPrefix(line, "prefix=") || strings.HasPrefix(line, "site_user=") {
			derivation = append(derivation, line)
		}
	}
	if len(derivation) != 5 {
		t.Fatalf("expected 5 site_user derivation lines in stepanel-sitectl, found %d: %q", len(derivation), derivation)
	}
	script := `site=$1` + "\n" + strings.Join(derivation, "\n") + "\nprintf '%s' \"$site_user\""
	for _, site := range sites {
		out, err := exec.Command(bash, "-c", script, "derive", site).Output()
		if err != nil {
			t.Fatalf("shell derivation for %q: %v", site, err)
		}
		if got, want := UnixUser(site), string(out); got != want {
			t.Errorf("UnixUser(%q) = %q, shell helper derives %q", site, got, want)
		}
	}
}

func TestWebGroupFromEnvSupportsCaddyAndDistributionGroups(t *testing.T) {
	for _, group := range []string{"caddy", "www-data", "apache", "nogroup"} {
		t.Run(group, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ste-panel.env")
			if err := os.WriteFile(path, []byte("STEPANEL_WEB_GROUP=\""+group+"\"\nSTEPANEL_PHP_SOCKET_ROOT=\"/run/php\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := WebGroupFromEnv(path, func(name string) bool { return name == group })
			if err != nil || got != group {
				t.Fatalf("WebGroupFromEnv() = %q, %v; want %q", got, err, group)
			}
		})
	}
}

func TestWebGroupFromEnvFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ste-panel.env")
	for _, tc := range []struct {
		name string
		mode os.FileMode
		body string
	}{
		{name: "missing value", mode: 0600, body: "STEPANEL_WEBSERVER=\"caddy\"\n"},
		{name: "group readable", mode: 0640, body: "STEPANEL_WEB_GROUP=\"caddy\"\n"},
		{name: "unknown group", mode: 0600, body: "STEPANEL_WEB_GROUP=\"missing\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			if got, err := WebGroupFromEnv(path, func(string) bool { return false }); err == nil || got != "" {
				t.Fatalf("WebGroupFromEnv() = %q, %v; want a fail-closed error", got, err)
			}
		})
	}
}
