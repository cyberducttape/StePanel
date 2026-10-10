package siteidentity

import (
	"os"
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

// TestShellHelperConsumesPersistedIdentity ensures the privileged helper no
// longer derives an account from customer-controlled site text.
func TestShellHelperConsumesPersistedIdentity(t *testing.T) {
	helper, err := os.ReadFile(filepath.Join("..", "..", "deploy", "integrations", "stepanel-sitectl"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(helper)
	if !strings.Contains(source, "site_user=$3") {
		t.Fatal("site helper does not consume the persisted account argument")
	}
	if strings.Contains(source, "site_user=\"sp-${prefix}-") && !strings.Contains(source, "STEPANEL_UNSAFE_LAB") {
		t.Fatal("site helper derives an account without an explicit unsafe-lab guard")
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
