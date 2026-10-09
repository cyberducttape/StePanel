package stepanel

import (
	"os"
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
		`install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-nosymfollow" /usr/local/sbin/stepanel-nosymfollow`,
		`install -m 0644 "$ROOT_DIR/deploy/stepanel-nosymfollow.service" /etc/systemd/system/stepanel-nosymfollow.service`,
		`systemctl enable --now stepanel-nosymfollow.service`,
	} {
		if !strings.Contains(source, line) {
			t.Fatalf("install.sh is missing authoritative identity contract %q", line)
		}
	}
}

func TestCaddyVHostDeniesSensitiveFilesAndRejectsSymlinks(t *testing.T) {
	sourceBytes, err := os.ReadFile("deploy/integrations/stepanel-caddy-vhostctl")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	for _, required := range []string{
		`find -P "$public_root" -xdev -type l`,
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
		`find -P "$site_root" -xdev -type l`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("site helper is missing protection %q", required)
		}
	}
	if !strings.Contains(string(mustReadTestFile(t, "deploy/stepanel-nosymfollow.service")), "stepanel-nosymfollow") {
		t.Fatal("nosymfollow systemd unit is not installed as a managed service")
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
