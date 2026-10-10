package stepanel

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestInstallerPersistsAuthoritativeWebIdentity(t *testing.T) {
	installer, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(installer)
	for _, line := range []string{
		`write_env STEPANEL_WEB_GROUP "$WEB_GROUP"`,
		`write_env STEPANEL_PHP_SOCKET_ROOT "$PHP_SOCKET_ROOT"`,
		// The retired /var/www nosymfollow mount broke virtualenv and
		// node_modules symlinks; upgrades must remove it.
		`systemctl disable --now stepanel-nosymfollow.service`,
		`umount /var/www`,
	} {
		if !strings.Contains(source, line) {
			t.Fatalf("install.sh is missing authoritative identity contract %q", line)
		}
	}
	if strings.Contains(source, "systemctl enable --now stepanel-nosymfollow.service") {
		t.Fatal("install.sh still enables the /var/www nosymfollow mount")
	}
}

func TestPythonVirtualenvLivesOutsideDocumentRoot(t *testing.T) {
	source := string(mustReadTestFile(t, "deploy/integrations/stepanel-appctl"))
	if !strings.Contains(source, `; venv="/var/www/sites/$site/.venv"`) || regexp.MustCompile(`(^|[^_])venv="\$root/\.venv"`).MatchString(source) {
		t.Fatal("Python virtualenv must live beside the document root, where vhost symlink checks do not apply")
	}
}

func TestCaddyVHostDeniesSensitiveFilesAndRejectsSymlinks(t *testing.T) {
	sourceBytes, err := os.ReadFile("deploy/integrations/stepanel-caddy-vhostctl")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	for _, required := range []string{
		`mount -o remount,bind,nosymfollow`,
		`x-systemd.requires-mounts-for=`,
		`/var/www/.stepanel-caddy-views`,
		`find -P "$public_root" -xdev -path "$public_root/node_modules" -prune -o -type l`,
		`^/(node_modules(/.*)?|`,
		`@stepanel_sensitive path_regexp`,
		`respond @stepanel_sensitive 404`,
		`file_server {`,
		`.env`,
		`.git`,
		`.htpasswd`,
		`\.(sql|sqlite`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Caddy helper is missing protection %q", required)
		}
	}
	if strings.Contains(source, "(?:") {
		t.Fatal("Caddy path regexp uses unsupported non-capturing groups")
	}
}

func TestSiteHelperUsesAuthoritativeWebIdentityAndRejectsSymlinks(t *testing.T) {
	sourceBytes, err := os.ReadFile("deploy/integrations/stepanel-sitectl")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	for _, required := range []string{
		`STEPANEL_WEB_GROUP`,
		`STEPANEL_PHP_SOCKET_ROOT`,
		`find -P "$site_root/public" -xdev -path "$site_root/public/node_modules" -prune -o -type l`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("site helper is missing protection %q", required)
		}
	}
	if strings.Contains(source, "getent group www-data") || strings.Contains(source, "getent group apache") {
		t.Fatal("site helper independently guesses the web-server group")
	}
}

func mustReadTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
