package siteidentity

import (
	"errors"
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

func TestWebGroupPrefersWWWDataThenApache(t *testing.T) {
	cases := []struct {
		present []string
		want    string
		err     error
	}{
		{[]string{"www-data", "apache"}, "www-data", nil},
		{[]string{"apache"}, "apache", nil},
		{nil, "", ErrNoWebGroup},
	}
	for _, tc := range cases {
		got, err := WebGroup(func(name string) bool {
			for _, present := range tc.present {
				if present == name {
					return true
				}
			}
			return false
		})
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("WebGroup(%v) = %q, %v; want %q, %v", tc.present, got, err, tc.want, tc.err)
		}
	}
}
