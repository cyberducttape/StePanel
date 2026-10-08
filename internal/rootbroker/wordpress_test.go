package rootbroker

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWordPressArgsAcceptsNamedOperations(t *testing.T) {
	cases := []struct {
		req  WordPressRequest
		want string
	}{
		{WordPressRequest{Action: "status"}, "core version --format=json"},
		{WordPressRequest{Action: "maintenance-status"}, "--skip-plugins --skip-themes maintenance-mode is-active"},
		{WordPressRequest{Action: "option-get", Name: "siteurl"}, "--skip-plugins --skip-themes option get siteurl"},
		{WordPressRequest{Action: "option-update", Name: "home", Value: "https://example.org"}, "--skip-plugins --skip-themes option update home https://example.org"},
		{WordPressRequest{Action: "option-update", Name: "active_plugins", Value: `["akismet/akismet.php","hello.php"]`}, `--skip-plugins --skip-themes option update active_plugins ["akismet/akismet.php","hello.php"] --format=json`},
		{WordPressRequest{Action: "search-replace", Search: "http://old.test", Replace: "https://new.test"}, "--skip-plugins --skip-themes search-replace http://old.test https://new.test --all-tables-with-prefix --precise --recurse-objects --skip-columns=guid --quiet"},
		{WordPressRequest{Action: "config-create", DBName: "site_wp", DBUser: "site_u", DBHost: "localhost:/run/mysqld/mysqld.sock", DBPrefix: "wp_", Secret: "s3cret-password!"}, "config create --dbname=site_wp --dbuser=site_u --dbhost=localhost:/run/mysqld/mysqld.sock --dbprefix=wp_ --prompt=dbpass --skip-check --skip-salts"},
		{WordPressRequest{Action: "config-set", Name: "DB_HOST", Value: "127.0.0.1:3306"}, "config set DB_HOST 127.0.0.1:3306 --type=constant"},
		{WordPressRequest{Action: "config-set-password", Secret: "s3cret-password!"}, "config set DB_PASSWORD --type=constant --prompt=value"},
	}
	for _, tc := range cases {
		args, err := WordPressArgs(&tc.req)
		if err != nil {
			t.Fatalf("%s: %v", tc.req.Action, err)
		}
		if got := strings.Join(args, " "); got != tc.want {
			t.Fatalf("%s args = %q, want %q", tc.req.Action, got, tc.want)
		}
		for _, arg := range args {
			if strings.Contains(arg, "s3cret") {
				t.Fatalf("%s placed the secret in argv: %q", tc.req.Action, args)
			}
		}
	}
}

// Values from uploaded archives and API callers must never become wp-cli
// options such as --exec or --require, which would load arbitrary code.
func TestWordPressArgsRejectsInjectedValues(t *testing.T) {
	for name, req := range map[string]WordPressRequest{
		"unknown action":         {Action: "eval"},
		"theme option":           {Action: "option-update", Name: "template", Value: "--exec=phpinfo();"},
		"theme traversal":        {Action: "option-update", Name: "stylesheet", Value: "../x"},
		"arbitrary option":       {Action: "option-update", Name: "admin_email", Value: "a@b.c"},
		"arbitrary option get":   {Action: "option-get", Name: "admin_email"},
		"plugin traversal":       {Action: "option-update", Name: "active_plugins", Value: `["../evil.php"]`},
		"plugin option":          {Action: "option-update", Name: "active_plugins", Value: `["--require=x.php"]`},
		"plugins not json":       {Action: "option-update", Name: "active_plugins", Value: `akismet/akismet.php`},
		"url option":             {Action: "option-update", Name: "home", Value: "--url=https://x.test"},
		"url scheme":             {Action: "search-replace", Search: "javascript:alert(1)", Replace: "https://x.test"},
		"url newline":            {Action: "search-replace", Search: "https://a.test\n--exec=x", Replace: "https://x.test"},
		"config constant":        {Action: "config-set", Name: "WP_DEBUG", Value: "true"},
		"config value option":    {Action: "config-set", Name: "DB_HOST", Value: "--exec=x"},
		"config create no pass":  {Action: "config-create", DBName: "a", DBUser: "b", DBHost: "localhost", DBPrefix: "wp_"},
		"config create bad host": {Action: "config-create", DBName: "a", DBUser: "b", DBHost: "-x", DBPrefix: "wp_", Secret: "password1234567890"},
		"password newline":       {Action: "config-set-password", Secret: "pass\nword"},
	} {
		if _, err := WordPressArgs(&req); err == nil {
			t.Errorf("%s: WordPressArgs accepted %#v", name, req)
		}
	}
}

func TestWordPressFencingAndLocking(t *testing.T) {
	for action, fenced := range map[string]bool{"status": false, "maintenance-status": false, "option-get": false, "maintenance-activate": true, "core-update": true, "config-set-password": true} {
		req := &Request{RequestType: "wordpress", WordPress: &WordPressRequest{Action: action, Site: "demo"}}
		if got := requestRequiresFencing(req); got != fenced {
			t.Errorf("%s requires fencing = %v, want %v", action, got, fenced)
		}
		if keys := requestLockKeys(req); len(keys) != 1 || keys[0] != "site:demo" {
			t.Errorf("%s lock keys = %v, want [site:demo]", action, keys)
		}
	}
}

func TestBrokerWordPressRunsThroughAppctl(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "appctl.log")
	helper := filepath.Join(root, "appctl")
	// Records argv and stdin; reports maintenance mode off with exit 1 and
	// treats exit 2 as a broken install.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\nread -r secret\nprintf 'stdin=%s\\n' \"$secret\" >> \"" + logPath + "\"\n" +
		"for arg; do last=$arg; done\n" +
		"case \"$last\" in is-active) exit \"${WP_ACTIVE_STATUS:-1}\" ;; siteurl) echo https://old.test ;; esac\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	broker, err := newTestBroker(t, root, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	broker.appctlPath = helper
	execute := func(req WordPressRequest) (*Response, WordPressResponse) {
		t.Helper()
		response, err := broker.Execute(context.Background(), &Request{RequestType: "wordpress", WordPress: &req})
		if err != nil {
			t.Fatal(err)
		}
		var details WordPressResponse
		if response.OK {
			if err := json.Unmarshal(response.Details, &details); err != nil {
				t.Fatal(err)
			}
		}
		return response, details
	}

	if response, details := execute(WordPressRequest{Action: "option-get", Site: "demo", Name: "siteurl"}); !response.OK || strings.TrimSpace(details.Output) != "https://old.test" {
		t.Fatalf("option-get = %#v %#v", response, details)
	}
	if response, details := execute(WordPressRequest{Action: "maintenance-status", Site: "demo"}); !response.OK || details.Active {
		t.Fatalf("maintenance-status with exit 1 = %#v %#v, want inactive", response, details)
	}
	t.Setenv("WP_ACTIVE_STATUS", "0")
	if response, details := execute(WordPressRequest{Action: "maintenance-status", Site: "demo"}); !response.OK || !details.Active {
		t.Fatalf("maintenance-status with exit 0 = %#v %#v, want active", response, details)
	}
	t.Setenv("WP_ACTIVE_STATUS", "2")
	if response, _ := execute(WordPressRequest{Action: "maintenance-status", Site: "demo"}); response.OK {
		t.Fatalf("maintenance-status with exit 2 = %#v, want failure", response)
	}
	if response, _ := execute(WordPressRequest{Action: "config-set-password", Site: "demo", Secret: "s3cret-password!"}); !response.OK {
		t.Fatalf("config-set-password = %#v", response)
	}
	if response, _ := execute(WordPressRequest{Action: "option-update", Site: "demo", Name: "template", Value: "--exec=x"}); response.OK {
		t.Fatal("broker accepted an injected theme value")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{
		"wp demo --skip-plugins --skip-themes option get siteurl\n",
		"wp demo config set DB_PASSWORD --type=constant --prompt=value\nstdin=s3cret-password!\n",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("appctl log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "--exec") {
		t.Fatalf("injected value reached appctl:\n%s", log)
	}
}
