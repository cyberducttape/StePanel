package rootbroker

import (
	"strings"
	"testing"
)

func TestHelperSchemaAcceptsWellFormedRequests(t *testing.T) {
	v := NewValidator("/var/www")
	public := "/var/www/sites/demo/public"
	release := "/var/www/sites/demo/.stepanel-release-20260101-abc"
	hash := "$2a$10$" + strings.Repeat("a", 53)
	image := "docker.io/library/node@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, action string
		args         []string
	}{
		{"appctl", "python-apply", []string{"demo", "3.12", public, "app:app", "8000", "2"}},
		{"appctl", "node-tool", []string{"demo", "install", "pnpm", public}},
		{"appctl", "composer-install", []string{"demo", public, "0", "1"}},
		{"appctl", "env-apply", []string{"demo"}},
		{"appctl", "worker-apply", []string{"demo", "queue", "laravel", public, "2", "256", "3"}},
		{"appctl", "resource-apply", []string{"demo", "100", "100", "900", "1024", "100", "256"}},
		{"appctl", "resource-apply", []string{"demo", "100", "100", "900", "1024", "100", "256", ""}},
		{"appctl", "resource-apply", []string{"demo", "100", "100", "900", "1024", "100", "256", "alice"}},
		{"appctl", "account-resource-apply", []string{"alice", "100", "100", "900", "1024", "100", "256"}},
		{"proxyctl", "apply", []string{"demo", "app.example.test", "127.0.0.1:3000"}},
		{"proxyctl", "apply", []string{"demo", "app.example.test", "10.0.0.5:8080"}},
		{"proxyctl", "delete", []string{"demo-app_example_test.conf"}},
		{"proxyctl", "reload", nil},
		{"sitectl", "access", []string{"demo", "1", "0"}},
		{"sitectl", "resources", []string{"demo", "8"}},
		{"sitectl", "quota", []string{"demo", "10240", "200000"}},
		{"sitectl", "runtime", []string{"demo", "8.4", "256M", "60", "64M", "64M", "1000", "1", "0", "E_ALL & ~E_DEPRECATED"}},
		{"vhostctl", "apply-auth", []string{"demo", "example.test", "staff", hash}},
		{"vhostctl", "delete", []string{"site-demo-example_test.conf"}},
		{"runnerctl", "build", []string{"demo", image, release, "/var/lib/ste-panel/apps/pipeline-123.sh", "100", "1024", "256", "none", "1073741824"}},
		{"runnerctl", "build", []string{"demo", image, public, "/var/lib/ste-panel/apps/runner-9.sh", "100", "1024", "256", "egress", "1073741824"}},
		{"gitctl", "clone", []string{"demo", "git@github.com:acme/site.git", "main", release, "github.com,gitlab.com"}},
		{"dbctl", "provision", []string{"demo_db", "demo_user", "demo", "utf8mb4"}},
		{"dbctl", "restore-wordpress", []string{"demo_wp", "wp_user", "demo"}},
		{"dbctl", "terminate", []string{"42"}},
		{"dbctl", "reconcile", nil},
	} {
		if err := v.validateHelperRequest(&HelperRequest{Name: tc.name, Action: tc.action, Args: tc.args}); err != nil {
			t.Errorf("%s/%s %q rejected: %v", tc.name, tc.action, tc.args, err)
		}
	}
}

func TestHelperSchemaRejectsOutOfContractArguments(t *testing.T) {
	v := NewValidator("/var/www")
	for _, tc := range []struct {
		why, name, action string
		args              []string
	}{
		{"undeclared action", "appctl", "task-apply", []string{"demo"}},
		{"undeclared helper", "rootctl", "exec", []string{"id"}},
		{"extra argument", "appctl", "env-apply", []string{"demo", "--force"}},
		{"missing argument", "sitectl", "access", []string{"demo", "1"}},
		{"other site's root", "appctl", "composer-install", []string{"demo", "/var/www/sites/victim/public", "0", "0"}},
		{"traversal root", "appctl", "node-tool", []string{"demo", "install", "npm", "/var/www/sites/demo/public/../../victim/public"}},
		{"relative root", "appctl", "node-tool", []string{"demo", "install", "npm", "sites/demo/public"}},
		{"unknown worker type", "appctl", "worker-apply", []string{"demo", "q", "bash", "/var/www/sites/demo/public", "1", "1", "1"}},
		{"metadata backend", "proxyctl", "apply", []string{"demo", "app.example.test", "169.254.169.254:80"}},
		{"public backend", "proxyctl", "apply", []string{"demo", "app.example.test", "8.8.8.8:53"}},
		{"bad port", "proxyctl", "apply", []string{"demo", "app.example.test", "127.0.0.1:70000"}},
		{"site flag value", "sitectl", "access", []string{"demo", "yes", "0"}},
		{"PHP workers out of range", "sitectl", "resources", []string{"demo", "100000"}},
		{"negative quota", "sitectl", "quota", []string{"demo", "-1", "5"}},
		{"release outside site", "gitctl", "clone", []string{"demo", "git@github.com:a/b.git", "main", "/var/www/sites/other/.stepanel-release-x", "github.com"}},
		{"https repository", "gitctl", "clone", []string{"demo", "https://github.com/a/b.git", "main", "/var/www/sites/demo/.stepanel-release-x", "github.com"}},
		{"option-like ref", "gitctl", "clone", []string{"demo", "git@github.com:a/b.git", "--upload-pack=x", "/var/www/sites/demo/.stepanel-release-x", "github.com"}},
		{"unpinned image", "runnerctl", "build", []string{"demo", "node:latest", "/var/www/sites/demo/public", "/var/lib/ste-panel/apps/runner-1.sh", "100", "1024", "256", "none", "1"}},
		{"arbitrary script", "runnerctl", "build", []string{"demo", "n@sha256:" + strings.Repeat("a", 64), "/var/www/sites/demo/public", "/etc/shadow", "100", "1024", "256", "none", "1"}},
		{"SQL in database name", "dbctl", "drop", []string{"x`; DROP DATABASE mysql; --"}},
		{"uppercase user", "dbctl", "rotate", []string{"db", "Root"}},
		{"newline", "dbctl", "list", []string{"demo\nx"}},
	} {
		if err := v.validateHelperRequest(&HelperRequest{Name: tc.name, Action: tc.action, Args: tc.args}); err == nil {
			t.Errorf("%s: %s/%s %q accepted", tc.why, tc.name, tc.action, tc.args)
		}
	}
}
