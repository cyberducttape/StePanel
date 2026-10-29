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
	// $site_private is the legacy site root, or a placed site's private state
	// directory (ADR-0007); both sit beside the document root.
	if !strings.Contains(source, `; venv="$site_private/.venv"`) || regexp.MustCompile(`(^|[^_])venv="\$root/\.venv"`).MatchString(source) {
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
		`/var/www/.stepanel-caddy-views`,
		`systemd-escape --path --suffix=mount`,
		`findmnt --task 1`,
		`/etc/systemd/system/%s`,
		`%s-nosymfollow.service`,
		`remove_legacy_fstab_target`,
		`RequiresMountsFor=%s %s`,
		`What=%s`,
		`Where=%s`,
		`Options=bind`,
		`ExecStart=/usr/bin/mount -o remount,bind,nosymfollow %s`,
		`RequiredBy=caddy.service`,
		`WantedBy=multi-user.target`,
		`NoNewPrivileges=yes`,
		`CapabilityBoundingSet=CAP_SYS_ADMIN`,
		`RestrictNamespaces=yes`,
		`observed mount options: ${options:-unavailable}`,
		`broker namespace mount:`,
		`PID 1 exact mount:`,
		`broker exact mount:`,
		`systemctl show "$(mount_unit "$view")" -p ActiveState`,
		`WantedBy=local-fs.target`,
		`systemctl enable "$unit"`,
		`systemctl enable "$flags_unit"`,
		`systemctl start "$unit"`,
		`systemctl restart "$flags_unit"`,
		`systemctl stop "$unit"`,
		`systemctl stop "$flags_unit"`,
		`systemctl disable "$unit"`,
		`systemctl disable "$flags_unit"`,
		`systemctl daemon-reload`,
		`view_mount_changed=1`,
		`systemctl restart "$service"`,
		`if (( view_mount_changed )); then`,
		`systemctl reload "$service"`,
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
		`respond @stepanel_sensitive_dirs 404`,
		`\.(sql|sqlite`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Caddy helper is missing protection %q", required)
		}
	}
	if strings.Contains(source, "write_fstab_target") || strings.Contains(source, "/etc/fstab is unavailable") {
		t.Fatal("Caddy view creation must not depend on writing /etc/fstab")
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
		"/database/export.sql", "/database/export.dump", "/debug/debug.log", "/archives/backup.tar.gz",
		"/archives/export.sql.bz2", "/archives/site.7z", "/keys/server.pem", "/keys/server.key",
		"/.ssh/id_ed25519", "/.aws/credentials", "/.gnupg/private-keys-v1.d/key",
		"/backups/site.tar.gz", "/nested/.htpasswd", "/node_modules/pkg/secret.js",
	} {
		if !policy.MatchString(path) {
			t.Errorf("Caddy sensitive-file matcher does not reject %q", path)
		}
	}
	// backup, backups and private are ordinary words in application routes,
	// so they are denied only for paths that exist in the document root.
	for _, path := range []string{"/index.php", "/assets/app.css", "/api/public.json", "/nested/image.png", "/private/", "/blog/backups"} {
		if policy.MatchString(path) {
			t.Errorf("Caddy sensitive-file matcher unexpectedly rejects ordinary content %q", path)
		}
	}
	dirs := regexp.MustCompile(`(?m)^\s*@stepanel_sensitive_dirs \{\n\s*path_regexp stepanel_sensitive_dirs (.+)\n\s*file \{path\} \{path\}/\n\s*\}$`).FindStringSubmatch(source)
	if len(dirs) != 2 {
		t.Fatal("Caddy sensitive-directory matcher must require the path to exist")
	}
	dirPolicy, err := regexp.Compile(dirs[1])
	if err != nil {
		t.Fatalf("Caddy sensitive-directory matcher is invalid: %v", err)
	}
	for _, path := range []string{"/backups/notes.txt", "/private/config.php", "/nested/backup/", "/PRIVATE/x"} {
		if !dirPolicy.MatchString(path) {
			t.Errorf("Caddy sensitive-directory matcher does not cover %q", path)
		}
	}
	for _, path := range []string{"/privacy", "/backup-plan.html", "/index.php"} {
		if dirPolicy.MatchString(path) {
			t.Errorf("Caddy sensitive-directory matcher unexpectedly covers %q", path)
		}
	}
}

func TestCaddyInstallSmokeAppliesRoutesThroughRootBroker(t *testing.T) {
	source := string(mustReadTestFile(t, "deploy/lab/install-smoke.sh"))
	for _, required := range []string{
		`apply_smoke_vhost()`,
		`runuser -u stepanel -- python3`,
		`"type": "vhost"`,
		`"webserver": "caddy"`,
		`"fencing": [{"resource_key": resource_key`,
		`INSERT INTO resource_locks`,
		`connection.connect("/run/stepanel-root-broker.sock")`,
		`apply_smoke_vhost "$site" ci-smoke.localhost`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Caddy installed-host smoke is missing broker-path assertion %q", required)
		}
	}
	if strings.Contains(source, `/usr/local/sbin/stepanel-vhostctl apply "$site" ci-smoke.localhost`) {
		t.Fatal("Caddy installed-host smoke bypasses the root broker mount namespace")
	}
}

func TestApacheVHostDeniesSensitiveFilesAndDirectories(t *testing.T) {
	sourceBytes, err := os.ReadFile("deploy/integrations/stepanel-vhostctl")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	files := regexp.MustCompile(`(?m)^\s*<FilesMatch "([^"]+)">`).FindStringSubmatch(source)
	directories := regexp.MustCompile(`(?m)^\s*<DirectoryMatch "([^"]+)">`).FindStringSubmatch(source)
	if len(files) != 2 || len(directories) != 2 {
		t.Fatalf("Apache vhost is missing sensitive-file/directory deny policy")
	}
	// The patterns are in an unquoted shell heredoc; Bash reduces each escaped
	// backslash pair to one before Apache receives the generated config.
	filePolicy, err := regexp.Compile(strings.ReplaceAll(files[1], `\\`, `\`))
	if err != nil {
		t.Fatalf("Apache sensitive-file policy is invalid: %v", err)
	}
	directoryPattern := strings.ReplaceAll(directories[1], "$public_root_pattern", "/var/www/sites/example/public")
	directoryPolicy, err := regexp.Compile(strings.ReplaceAll(directoryPattern, `\\`, `\`))
	if err != nil {
		t.Fatalf("Apache sensitive-directory policy is invalid: %v", err)
	}
	// A hosting-account document root (ADR-0007) is escaped by the helper.
	placedPattern := strings.ReplaceAll(directories[1], "$public_root_pattern", `/home/alice/shop\.example\.org`)
	placedPolicy, err := regexp.Compile(strings.ReplaceAll(placedPattern, `\\`, `\`))
	if err != nil {
		t.Fatalf("Apache sensitive-directory policy is invalid for a placed site: %v", err)
	}
	if !placedPolicy.MatchString("/home/alice/shop.example.org/.git/config") || placedPolicy.MatchString("/home/alice/shopXexample.org/.git/config") {
		t.Error("Apache sensitive-directory policy does not anchor to the escaped placed document root")
	}
	for _, name := range []string{
		".env", ".env.production", "export.sql", "export.dump", "debug.log", "debug.log.1", "export.sql.gz",
		"backup.tar.zst", "private.pem", "private.key", "credentials.p12",
	} {
		if !filePolicy.MatchString(name) {
			t.Errorf("Apache sensitive-file policy does not reject %q", name)
		}
	}
	for _, path := range []string{
		"/var/www/sites/example/public/.git/config",
		"/var/www/sites/example/public/nested/.ssh/id_ed25519",
		"/var/www/sites/example/public/backups/weekly/archive.zip",
		"/var/www/sites/example/public/node_modules/package/secret.js",
	} {
		if !directoryPolicy.MatchString(path) {
			t.Errorf("Apache sensitive-directory policy does not reject %q", path)
		}
	}
	for _, name := range []string{"index.php", "app.css", "api.json"} {
		if filePolicy.MatchString(name) {
			t.Errorf("Apache sensitive-file policy unexpectedly rejects %q", name)
		}
	}
	for _, path := range []string{
		"/var/www/sites/example/public/assets/app.js",
		"/var/www/sites/example/public/.well-known/acme-challenge/token",
	} {
		if directoryPolicy.MatchString(path) {
			t.Errorf("Apache sensitive-directory policy unexpectedly rejects %q", path)
		}
	}
}

func TestCaddyFastCGIUsesCanonicalSiteRoot(t *testing.T) {
	source := string(mustReadTestFile(t, "deploy/integrations/stepanel-caddy-vhostctl"))
	if !strings.Contains(source, `if [[ -f "$public_root/index.php" && ! -L "$public_root/index.php" ]]`) ||
		!strings.Contains(source, `@stepanel_php_root path /\n\trewrite @stepanel_php_root /index.php\n`) {
		t.Fatal("Caddy must route / to an existing, non-symlink PHP front controller while preserving query strings")
	}
	if !strings.Contains(source, `view_path() { printf '%s/%s\n' "$view_root" "$1"; }`) ||
		!strings.Contains(source, `document_root="$view_root_path/$site_directory"`) ||
		!strings.Contains(source, `"$domain" "$document_root"`) {
		t.Fatal("Caddy must serve the document-root child through the stable isolated nosymfollow parent view")
	}
	if !strings.Contains(source, `php_fastcgi unix/%s {\n\t\troot %s\n\t}`) {
		t.Fatal("Caddy FastCGI must send the canonical site root to the per-site PHP-FPM pool")
	}
	if !strings.Contains(source, `"$socket" "$public_root" "$document_root/.env"`) {
		t.Fatal("Caddy FastCGI root must match PHP-FPM's canonical site path, while static file hides stay on the served view child")
	}
}

func TestPHPFPMStatusIsPrivateAndEnabledForManagedPools(t *testing.T) {
	pool := string(mustReadTestFile(t, "deploy/integrations/stepanel-sitectl"))
	caddy := string(mustReadTestFile(t, "deploy/integrations/stepanel-caddy-vhostctl"))
	apache := string(mustReadTestFile(t, "deploy/integrations/stepanel-vhostctl"))
	if !strings.Contains(pool, "pm.status_path = /stepanel-fpm-status") {
		t.Fatal("managed PHP-FPM pools must configure their private status path")
	}
	if !strings.Contains(pool, "request_slowlog_timeout = 5s") || !strings.Contains(pool, "slowlog = /dev/null") {
		t.Fatal("managed PHP-FPM pools must count five-second slow requests without persisting tenant stack traces")
	}
	caddyDeny := strings.Index(caddy, "@stepanel_fpm_status path /stepanel-fpm-status")
	caddyFastCGI := strings.Index(caddy, "php_fastcgi unix/%s")
	if caddyDeny < 0 || caddyFastCGI < 0 || caddyDeny > caddyFastCGI || !strings.Contains(caddy, "respond @stepanel_fpm_status 404") {
		t.Fatal("Caddy must deny the private FPM status path before FastCGI handling")
	}
	if !strings.Contains(apache, "<LocationMatch \"^/stepanel-fpm-status(/|$)\">\n        Require all denied\n    </LocationMatch>") {
		t.Fatal("Apache must deny external requests to the private FPM status path")
	}
}

func TestCaddyLegacyViewCleanupNeverRemovesMountedSiteDocumentRoot(t *testing.T) {
	source := string(mustReadTestFile(t, "deploy/integrations/stepanel-caddy-vhostctl"))
	cleanupStart := strings.Index(source, "remove_legacy_view() {")
	if cleanupStart < 0 {
		t.Fatal("Caddy legacy-view cleanup function is missing")
	}
	cleanupEnd := strings.Index(source[cleanupStart:], "\n}\n")
	if cleanupEnd < 0 {
		t.Fatal("Caddy legacy-view cleanup function is unterminated")
	}
	cleanup := source[cleanupStart : cleanupStart+cleanupEnd]
	if !strings.Contains(cleanup, `remove_mount_unit "$old"`) ||
		!strings.Contains(cleanup, `if [[ $(host_findmnt -no TARGET -T "$view" 2>/dev/null || true) != "$view" ]]`) ||
		!strings.Contains(cleanup, `rmdir "$old"`) {
		t.Fatal("Caddy legacy cleanup must only remove the detached mountpoint, never a source document root under the stable parent view")
	}
}

func TestCaddyRouteIsRemovedBeforeItsViewMount(t *testing.T) {
	source := string(mustReadTestFile(t, "deploy/integrations/stepanel-caddy-vhostctl"))
	deleteStart := strings.Index(source, `if [[ $action == delete ]]; then`)
	if deleteStart < 0 {
		t.Fatal("Caddy route deletion branch is missing")
	}
	deleteEnd := strings.Index(source[deleteStart:], "\nsite=${2:-}")
	if deleteEnd < 0 {
		t.Fatal("Caddy route deletion branch is unterminated")
	}
	deletion := source[deleteStart : deleteStart+deleteEnd]
	removeRoute := strings.Index(deletion, `rm -f "$path"`)
	activate := strings.Index(deletion, `! activate_caddy_after_route_delete "$view_was_mounted"`)
	removeView := strings.Index(deletion, `if ! remove_view "$site"; then`)
	if removeRoute < 0 || activate < 0 || removeView < 0 || removeRoute > activate || activate > removeView {
		t.Fatal("Caddy must remove and activate the route while its document-root view is mounted")
	}
	if !strings.Contains(source, `activate_caddy_after_route_delete() {
  local view_was_mounted=$1
  if (( view_was_mounted )); then
    systemctl restart "$service"
  else
    systemctl reload "$service"
  fi
}`) {
		t.Fatal("route deletion must propagate only a failed restart or reload")
	}
	if !strings.Contains(deletion[removeView:], `mv -f "$backup" "$path"`) ||
		!strings.Contains(deletion[removeView:], `ensure_view "$site" "$site_home"`) {
		t.Fatal("failed Caddy view cleanup must restore the route and its protected view")
	}

	cleanupStart := strings.Index(source, "remove_mount_unit() {")
	if cleanupStart < 0 {
		t.Fatal("Caddy mount-unit cleanup function is missing")
	}
	cleanupEnd := strings.Index(source[cleanupStart:], "\n}\n")
	if cleanupEnd < 0 {
		t.Fatal("Caddy mount-unit cleanup function is missing or unterminated")
	}
	cleanup := source[cleanupStart : cleanupStart+cleanupEnd]
	disable := strings.Index(cleanup, `systemctl disable "$flags_unit"`)
	reload := strings.Index(cleanup, "reload_mount_units")
	stop := strings.Index(cleanup, `stop_managed_mount "$target"`)
	if disable < 0 || reload < 0 || stop < 0 || disable > reload || reload > stop {
		t.Fatal("Caddy must remove and reload the RequiredBy edge before stopping its policy unit")
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
		`find -P "$public_root" -xdev -path "$public_root/node_modules" -prune -o -type l`,
		`stepanel_site_location "$site"`,
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
