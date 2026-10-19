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
		`^/(.*/)?(node_modules(/.*)?|`,
		`@stepanel_sensitive path_regexp`,
		`respond @stepanel_sensitive 404`,
		`file_server {`,
		`.env`,
		`.git`,
		`.htpasswd`,
		`\.ssh(/.*)?`,
		`\.aws(/.*)?`,
		`\.gnupg(/.*)?`,
		`(backup|backups|private)(/.*)?`,
		`\.(sql|sqlite`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Caddy helper is missing protection %q", required)
		}
	}
	if strings.Contains(source, "(?:") {
		t.Fatal("Caddy path regexp uses unsupported non-capturing groups")
	}
	matcher := regexp.MustCompile(`(?m)^\s*@stepanel_sensitive path_regexp stepanel_sensitive (.+)$`).FindStringSubmatch(source)
	if len(matcher) != 2 {
		t.Fatal("Caddy sensitive-file matcher was not found")
	}
	policy, err := regexp.Compile(matcher[1])
	if err != nil {
		t.Fatalf("Caddy sensitive-file matcher is invalid: %v", err)
	}
	for _, path := range []string{
		"/.env", "/nested/.env.production", "/nested/.git/config", "/deep/path/.svn/entries",
		"/database/export.sql", "/database/export.dump", "/archives/backup.tar.gz",
		"/archives/export.sql.bz2", "/archives/site.7z", "/keys/server.pem", "/keys/server.key",
		"/.ssh/id_ed25519", "/.aws/credentials", "/.gnupg/private-keys-v1.d/key",
		"/backups/site.tar.gz", "/private/config.php", "/nested/.htpasswd", "/node_modules/pkg/secret.js",
	} {
		if !policy.MatchString(path) {
			t.Errorf("Caddy sensitive-file matcher does not reject %q", path)
		}
	}
	for _, path := range []string{"/index.php", "/assets/app.css", "/api/public.json", "/nested/image.png"} {
		if policy.MatchString(path) {
			t.Errorf("Caddy sensitive-file matcher unexpectedly rejects ordinary content %q", path)
		}
	}
}

func TestCaddyFastCGIUsesCanonicalSiteRoot(t *testing.T) {
	source := string(mustReadTestFile(t, "deploy/integrations/stepanel-caddy-vhostctl"))
	if !strings.Contains(source, `if [[ -f "$public_root/index.php" && ! -L "$public_root/index.php" ]]`) ||
		!strings.Contains(source, `@stepanel_php_root path /\n\trewrite @stepanel_php_root /index.php\n`) {
		t.Fatal("Caddy must route / to an existing, non-symlink PHP front controller while preserving query strings")
	}
	if !strings.Contains(source, `"$domain" "$view_root_path"`) {
		t.Fatal("Caddy must continue serving through the isolated nosymfollow view")
	}
	if !strings.Contains(source, `php_fastcgi unix/%s {\n\t\troot %s\n\t}`) {
		t.Fatal("Caddy FastCGI must send the canonical site root to the per-site PHP-FPM pool")
	}
	if !strings.Contains(source, `"$socket" "$public_root" "$view_root_path/.env"`) {
		t.Fatal("Caddy FastCGI root must match PHP-FPM's canonical site path, while static file hides stay on the view")
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
