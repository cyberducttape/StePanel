package stepanel

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/recovery"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

const proofTestConfig = `<?php
define( 'DB_NAME', 'shop_db' );
define( 'DB_USER', 'shop_user' );
define( 'DB_PASSWORD', 'original-secret' );
define( 'DB_HOST', 'db.internal' );
$table_prefix = 'wp_';
require_once ABSPATH . 'wp-settings.php';
`

type wordpressProofFixture struct {
	app     *App
	root    string
	webRoot string
	stage   string
	log     string
}

// newWordPressProofFixture builds an extracted WordPress backup stage and an
// App whose site, database, and vhost helpers are recording scripts.
func newWordPressProofFixture(t *testing.T, config string) *wordpressProofFixture {
	t.Helper()
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	stage := filepath.Join(root, "imports", "backup-rehearsal-1")
	for _, dir := range []string{filepath.Join(webRoot, "sites"), filepath.Join(stage, "site", "public"), filepath.Join(stage, "databases")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(stage, "site", "public", "wp-config.php"):   config,
		filepath.Join(stage, "site", "public", "wp-settings.php"): "<?php\n",
		filepath.Join(stage, "site", "public", "index.php"):       "<?php\n",
		filepath.Join(stage, "databases", "shop_db.sql"):          "CREATE TABLE wp_options (option_name TEXT);\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(root, "helpers.log")
	writeHelper := func(name, body string) string {
		path := filepath.Join(root, name)
		script := "#!/usr/bin/env bash\necho \"" + name + " $*\" >> \"" + log + "\"\n" + body + "\nexit 0\n"
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	sitectl := writeHelper("sitectl", `case "$1" in
  prepare-root) mkdir -p "`+webRoot+`/sites/$2" ;;
  delete) rm -rf "`+webRoot+`/sites/$2" ;;
esac`)
	dbctl := writeHelper("dbctl", `[[ "$1" == restore-wordpress ]] && cat > /dev/null`)
	vhostctl := writeHelper("vhostctl", "")
	app := &App{
		Config: Config{
			WebRoot:                webRoot,
			ImportRoot:             filepath.Join(root, "imports"),
			RecoveryRoot:           filepath.Join(root, "recovery"),
			AuditLog:               filepath.Join(root, "audit.jsonl"),
			SiteCtl:                sitectl,
			DBCtl:                  dbctl,
			VHostCtl:               vhostctl,
			WebServer:              "apache",
			DBEngine:               "mariadb",
			WordPressRecoveryProof: recoveryProofModeManual,
		},
		Auth: Auth{Username: "admin"},
	}
	return &wordpressProofFixture{app: app, root: root, webRoot: webRoot, stage: stage, log: log}
}

func (f *wordpressProofFixture) helperCalls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// serveProofWebServer stands in for the host web server on the probe's
// loopback port.
func serveProofWebServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	original := wordpressProofPorts
	wordpressProofPorts.http = port
	t.Cleanup(func() { wordpressProofPorts = original })
}

func TestWordPressProofHTTPSVerifiesTrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wp-login.php" {
			_, _ = w.Write([]byte(`<form><input name="log"></form>`))
			return
		}
		_, _ = w.Write([]byte(`<link href="/wp-content/themes/x/style.css">`))
	}))
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	original := wordpressProofPorts
	wordpressProofPorts.https = port
	t.Cleanup(func() { wordpressProofPorts = original })
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	checks, err := probeWordPressWithRoots(t.Context(), "https", "127.0.0.1", roots)
	if err != nil {
		t.Fatalf("trusted HTTPS proof probe failed: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("HTTPS proof checks = %v, want home and login checks", checks)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := probeWordPress(ctx, "https", "127.0.0.1"); err == nil {
		t.Fatal("HTTPS proof accepted an untrusted certificate")
	}
}

func (f *wordpressProofFixture) assertTornDown(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.webRoot, "sites"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if isRecoveryProofSiteName(entry.Name()) {
			t.Fatalf("proof site %s was not removed", entry.Name())
		}
	}
	transactions, err := os.ReadDir(f.app.Config.RecoveryRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(transactions) != 0 {
		t.Fatalf("proof left recovery transactions: %v", transactions)
	}
	calls := f.helperCalls(t)
	for _, want := range []string{"vhostctl delete site-stepanel-proof-", "dbctl cleanup-wordpress spproof_", "sitectl delete stepanel-proof-"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("teardown did not call %q; calls:\n%s", want, calls)
		}
	}
}

func TestWordPressProofRestoresAndServesThrowawaySite(t *testing.T) {
	f := newWordPressProofFixture(t, proofTestConfig)
	var servedConfig string
	serveProofWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		site := strings.TrimSuffix(r.Host, ".localhost")
		if !isRecoveryProofSiteName(site) {
			http.Error(w, "unexpected host "+r.Host, http.StatusBadRequest)
			return
		}
		data, err := os.ReadFile(filepath.Join(f.webRoot, "sites", site, "public", "wp-config.php"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		servedConfig = string(data)
		if r.URL.Path == "/wp-login.php" {
			_, _ = w.Write([]byte(`<form><input name="log"></form>`))
			return
		}
		_, _ = w.Write([]byte(`<link href="/wp-content/themes/x/style.css">`))
	})
	result, err := f.app.proveWordPressRestore(context.Background(), f.stage, []string{"shop_db"})
	if err != nil {
		t.Fatalf("proof failed: %v; calls:\n%s", err, f.helperCalls(t))
	}
	if !isRecoveryProofSiteName(result.Site) || result.Hostname != result.Site+".localhost" || len(result.Checks) != 5 {
		t.Fatalf("result = %+v", result)
	}
	for _, want := range []string{"'DB_NAME', \"" + result.Database + "\"", "'DB_USER', \"" + result.Database + "\"", "'DB_HOST', \"localhost\"", "define('WP_HOME', \"http://" + result.Hostname + "\")", "define('WP_SITEURL', \"http://" + result.Hostname + "\")"} {
		if !strings.Contains(servedConfig, want) {
			t.Fatalf("served wp-config.php lacks %q:\n%s", want, servedConfig)
		}
	}
	if strings.Contains(servedConfig, "original-secret") || strings.Contains(servedConfig, "db.internal") {
		t.Fatal("served configuration kept the original database credentials")
	}
	calls := f.helperCalls(t)
	order := []string{"sitectl prepare-root", "sitectl seal", "dbctl restore-wordpress " + result.Database, "vhostctl apply " + result.Site + " " + result.Hostname, "vhostctl delete", "dbctl cleanup-wordpress", "sitectl delete"}
	last := -1
	for _, step := range order {
		index := strings.Index(calls, step)
		if index <= last {
			t.Fatalf("step %q out of order; calls:\n%s", step, calls)
		}
		last = index
	}
	f.assertTornDown(t)
}

func TestWordPressProofPlanAcceptsCPMoveArchiveLayout(t *testing.T) {
	stage := t.TempDir()
	public := filepath.Join(stage, "homedir", "public_html")
	if err := os.MkdirAll(public, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stage, "mysql"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(public, "wp-config.php"):       proofTestConfig,
		filepath.Join(public, "wp-settings.php"):     "<?php\n",
		filepath.Join(stage, "mysql", "shop_db.sql"): "CREATE TABLE wp_options (option_name TEXT);\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := planWordPressProof(Config{DBEngine: "mariadb", WebServer: "apache"}, stage, []string{"shop_db"})
	if err != nil {
		t.Fatalf("plan cpmove WordPress proof: %v", err)
	}
	if plan.public != public || plan.dump != filepath.Join(stage, "mysql", "shop_db.sql") {
		t.Fatalf("proof plan paths = public %q dump %q", plan.public, plan.dump)
	}
}

func TestWordPressProofFailsAndTearsDownWhenDatabaseIsUnreachable(t *testing.T) {
	f := newWordPressProofFixture(t, proofTestConfig)
	serveProofWebServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<h1>Error establishing a database connection</h1>"))
	})
	_, err := f.app.proveWordPressRestore(context.Background(), f.stage, []string{"shop_db"})
	if err == nil || !strings.Contains(err.Error(), "could not connect to the restored database") {
		t.Fatalf("proof error = %v, want database connection failure", err)
	}
	f.assertTornDown(t)
}

func TestWordPressProofAuditFailsWhenTeardownFailsAfterSuccessfulProbe(t *testing.T) {
	f := newWordPressProofFixture(t, proofTestConfig)
	if err := os.WriteFile(f.app.Config.VHostCtl, []byte("#!/usr/bin/env bash\nif [[ $1 == delete ]]; then exit 1; fi\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	serveProofWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wp-login.php" {
			_, _ = w.Write([]byte(`<form><input name="log"></form>`))
			return
		}
		_, _ = w.Write([]byte(`<link href="/wp-content/themes/x/style.css">`))
	})
	_, err := f.app.proveWordPressRestore(context.Background(), f.stage, []string{"shop_db"})
	if err == nil || !strings.Contains(err.Error(), "startup recovery will retry") {
		t.Fatalf("proof error = %v, want durable cleanup failure", err)
	}
	want := "recovery.proof.site.initiated,recovery.proof.site.failed"
	if got := strings.Join(auditActions(t, f.app.Config.AuditLog), ","); got != want {
		t.Fatalf("proof audit actions = %s, want %s", got, want)
	}
}

func TestWordPressProofReservesSiteVolumeBeforeCopy(t *testing.T) {
	f := newWordPressProofFixture(t, proofTestConfig)
	f.app.Config.MinFreeBytes = 1 << 62
	_, err := f.app.proveWordPressRestore(context.Background(), f.stage, []string{"shop_db"})
	if err == nil || !strings.Contains(err.Error(), "admit WordPress recovery proof site copy") {
		t.Fatalf("proof error = %v, want site-volume capacity admission failure", err)
	}
	if calls := f.helperCalls(t); calls != "" {
		t.Fatalf("capacity failure reached privileged helpers:\n%s", calls)
	}
	entries, readErr := os.ReadDir(filepath.Join(f.webRoot, "sites"))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("site tree was created before capacity admission: entries=%v err=%v", entries, readErr)
	}
}

func TestWordPressProofTreeSizeRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "index.php")
	if err := os.WriteFile(file, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	size, err := wordpressProofTreeSize(context.Background(), root)
	if err != nil || size != 5 {
		t.Fatalf("tree size = %d, err=%v; want 5", size, err)
	}
	if err := os.Symlink(file, filepath.Join(root, "linked.php")); err != nil {
		t.Fatal(err)
	}
	if _, err := wordpressProofTreeSize(context.Background(), root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink tree error = %v, want rejection", err)
	}
}

func TestWordPressProofRejectsRedirectToCustomerDomain(t *testing.T) {
	f := newWordPressProofFixture(t, proofTestConfig)
	serveProofWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://customer.example/", http.StatusMovedPermanently)
	})
	_, err := f.app.proveWordPressRestore(context.Background(), f.stage, []string{"shop_db"})
	if !errors.Is(err, errProofRedirectedAway) {
		t.Fatalf("proof error = %v, want redirect refusal", err)
	}
	f.assertTornDown(t)
}

func TestWordPressProofDetectsInstallerForMissingTables(t *testing.T) {
	f := newWordPressProofFixture(t, proofTestConfig)
	serveProofWebServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wp-admin/install.php" {
			http.Redirect(w, r, "/wp-admin/install.php", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("WordPress installation"))
	})
	_, err := f.app.proveWordPressRestore(context.Background(), f.stage, []string{"shop_db"})
	if err == nil || !strings.Contains(err.Error(), "installer") {
		t.Fatalf("proof error = %v, want installer detection", err)
	}
	f.assertTornDown(t)
}

func TestWordPressProofReportsUnsupportedBackups(t *testing.T) {
	for _, test := range []struct {
		name      string
		config    string
		databases []string
		mutate    func(*wordpressProofFixture)
	}{
		{name: "not WordPress", config: proofTestConfig, databases: []string{"shop_db"}, mutate: func(f *wordpressProofFixture) {
			_ = os.Remove(filepath.Join(f.stage, "site", "public", "wp-config.php"))
		}},
		{name: "database not in backup", config: proofTestConfig, databases: []string{"other_db"}},
		{name: "credentials from environment", config: strings.Replace(proofTestConfig, "'original-secret'", "getenv('DB_PASSWORD')", 1), databases: []string{"shop_db"}},
		{name: "PostgreSQL host", config: proofTestConfig, databases: []string{"shop_db"}, mutate: func(f *wordpressProofFixture) {
			f.app.Config.DBEngine = "postgresql"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newWordPressProofFixture(t, test.config)
			if test.mutate != nil {
				test.mutate(f)
			}
			_, err := f.app.proveWordPressRestore(context.Background(), f.stage, test.databases)
			if !errors.Is(err, errRecoveryProofUnsupported) {
				t.Fatalf("proof error = %v, want unsupported", err)
			}
			if calls := f.helperCalls(t); calls != "" {
				t.Fatalf("unsupported backup reached privileged helpers:\n%s", calls)
			}
		})
	}
}

// A crash leaves an uncommitted proof transaction; startup recovery removes
// its route, database, and site through the same helpers.
func TestStartupRecoveryRemovesInterruptedWordPressProof(t *testing.T) {
	f := newWordPressProofFixture(t, proofTestConfig)
	site := recoveryProofSitePrefix + "deadbeef"
	canonical := filepath.Join(f.webRoot, "sites", site, "public")
	access, err := f.app.authorizeDurableSiteJob(site, "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	txn, err := beginManagedSiteTransaction(context.Background(), f.app.Config, canonical, recoveryProofTransactionKind, access)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(canonical, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := txn.TrackDatabase(ManagedDatabase{Name: "spproof_deadbeef", User: "spproof_deadbeef", Kind: "wordpress"}); err != nil {
		t.Fatal(err)
	}
	route := "site-" + site + "-" + site + "_localhost.conf"
	if err := txn.TrackRoute(route); err != nil {
		t.Fatal(err)
	}
	if err := txn.MarkRouteApplied(route); err != nil {
		t.Fatal(err)
	}
	manager, err := siteauthority.NewDefaultManager(f.webRoot)
	if err != nil {
		t.Fatal(err)
	}
	if failures := recoverUncleanShutdown(f.app.Config, manager, nil); len(failures) != 0 {
		t.Fatalf("startup recovery failures: %v", failures)
	}
	calls := f.helperCalls(t)
	for _, want := range []string{"vhostctl delete " + route, "dbctl cleanup-wordpress spproof_deadbeef spproof_deadbeef", "sitectl delete " + site} {
		if !strings.Contains(calls, want) {
			t.Fatalf("recovery did not call %q; calls:\n%s", want, calls)
		}
	}
	if _, err := os.Stat(filepath.Join(f.webRoot, "sites", site)); !os.IsNotExist(err) {
		t.Fatalf("interrupted proof site survived recovery: %v", err)
	}
}

func TestRecoveryProofSitePrefixIsReserved(t *testing.T) {
	if validSiteName(recoveryProofSitePrefix + "abc") {
		t.Fatal("site creation accepts the reserved recovery proof prefix")
	}
	if userFromArchiveName("cpmove-"+recoveryProofSitePrefix+"abc.tar.gz") != "" {
		t.Fatal("cpmove import derives a reserved recovery proof site name")
	}
	if !validSiteName("stepanel-shop") {
		t.Fatal("ordinary site name rejected")
	}
}

func TestTrackedRouteNamesAreBoundToTheirSite(t *testing.T) {
	for name, want := range map[string]bool{
		"site-shop-shop_example_com.conf":  true,
		"site-shop-shop_example_com.caddy": true,
		"site-other-shop_example.conf":     false,
		"site-shop-../x.conf":              false,
		"site-shop-.conf":                  false,
		"site-shop-a.txt":                  false,
	} {
		if got := validTrackedRouteName("shop", name); got != want {
			t.Errorf("validTrackedRouteName(shop, %q) = %v, want %v", name, got, want)
		}
	}
}

// A crash after stepanel-sitectl delete but before the transaction closed
// leaves a prepared transaction for a site that no longer exists; recovery
// must close it instead of failing on every restart.
func TestStartupRecoveryClosesProofWhoseSiteWasAlreadyRemoved(t *testing.T) {
	f := newWordPressProofFixture(t, proofTestConfig)
	site := recoveryProofSitePrefix + "cafef00d"
	access, err := f.app.authorizeDurableSiteJob(site, "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(f.webRoot, "sites", site, "public")
	if _, err := beginManagedSiteTransaction(context.Background(), f.app.Config, canonical, recoveryProofTransactionKind, access); err != nil {
		t.Fatal(err)
	}
	manager, err := siteauthority.NewDefaultManager(f.webRoot)
	if err != nil {
		t.Fatal(err)
	}
	if failures := recoverUncleanShutdown(f.app.Config, manager, nil); len(failures) != 0 {
		t.Fatalf("startup recovery failures: %v", failures)
	}
	if failures := recoverUncleanShutdown(f.app.Config, manager, nil); len(failures) != 0 {
		t.Fatalf("second startup recovery failures: %v", failures)
	}
}

func TestRehearsalRecordsWhyWordPressProofWasSkipped(t *testing.T) {
	f := newRecoveryFixture(t)
	writeTestFile(t, filepath.Join(f.app.Config.WebRoot, "sites", "account", "public", "wp-config.php"), proofTestConfig)
	writeTestFile(t, filepath.Join(f.app.Config.WebRoot, "sites", "account", "public", "wp-settings.php"), "<?php\n")
	created, err := CreateSiteBackup(Config{WebRoot: f.app.Config.WebRoot, BackupRoot: f.app.Config.BackupRoot, BackupSigningKey: f.app.Config.BackupSigningKey}, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "helper")
	marker := helper + ".called"
	if err := os.WriteFile(helper, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.app.Config.SiteCtl, f.app.Config.DBCtl, f.app.Config.VHostCtl = helper, helper, helper
	f.app.Config.RecoveryRoot = filepath.Join(t.TempDir(), "recovery")
	f.app.Config.WebServer, f.app.Config.DBEngine = "caddy", "mariadb"
	payload, err := json.Marshal(durableBackupRehearsalRequest{Site: "account", Backup: filepath.Base(created.Path), Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.app.Jobs.Enqueue("backup.rehearsal", "account", "", payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	output, err := f.app.handleBackupRehearsalJob(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	var result backupRehearsalResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.ApplicationProof != nil || !strings.Contains(result.ApplicationProofSkipped, "does not include the database") {
		t.Fatalf("rehearsal result = %+v, want a recorded skip reason", result)
	}
	if status := f.status(t); status.LastPassed == nil || status.LastPassed.Level == recovery.LevelApplication {
		t.Fatalf("skipped proof raised the rehearsal level: %+v", status.LastPassed)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported backup reached privileged helpers")
	}
}

func TestWordPressProofModes(t *testing.T) {
	cfg := Config{SiteCtl: "s", DBCtl: "d", VHostCtl: "v", WebRoot: "/w", RecoveryRoot: "/r"}
	for _, test := range []struct {
		mode      string
		scheduled bool
		want      bool
	}{
		{recoveryProofModeManual, false, true},
		{recoveryProofModeManual, true, false},
		{recoveryProofModeAlways, true, true},
		{recoveryProofModeOff, false, false},
	} {
		cfg.WordPressRecoveryProof = test.mode
		if got := wordpressProofEnabled(cfg, test.scheduled); got != test.want {
			t.Errorf("mode %s scheduled=%v enabled = %v, want %v", test.mode, test.scheduled, got, test.want)
		}
	}
	cfg.WordPressRecoveryProof, cfg.VHostCtl = recoveryProofModeAlways, ""
	if wordpressProofEnabled(cfg, false) {
		t.Fatal("proof enabled without a vhost helper")
	}
}
