package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePromotionHost stands in for the verified-backup and database-clone
// helpers. Everything else in a promotion (recovery journal, SiteManager
// staging and activation, file copy, seal helper, WP-CLI, promotion record)
// runs for real against a temporary web root.
type fakePromotionHost struct {
	failBackup, failClone bool
	backups               int
	clones                []string
}

func (h *fakePromotionHost) SafetyBackup(_ context.Context, a *App, access SiteCapability) (BackupResult, error) {
	h.backups++
	if h.failBackup {
		return BackupResult{}, errors.New("injected backup failure")
	}
	return BackupResult{Path: filepath.Join(a.Config.BackupRoot, access.Site()+"-safety.tar.gz")}, nil
}

func (h *fakePromotionHost) CloneDatabase(_ context.Context, _ *App, source, target, user, _ string, _ SiteCapability) error {
	h.clones = append(h.clones, source+"->"+target+"/"+user)
	if h.failClone {
		return errors.New("injected clone failure")
	}
	return nil
}

type promotionFixture struct {
	app     *App
	host    *fakePromotionHost
	webRoot string
	log     string
}

// fakeWPCLI keeps wp-config values in .wp-fake-config inside --path, so the
// staging configuration travels with the copied files exactly as wp-config.php
// would. FAKE_WP_FAIL names a substring of the wp argv that should fail.
const fakeWPCLI = `#!/usr/bin/env bash
path="${1#--path=}"; shift; shift
echo "wp $*" >> "$FAKE_LOG"
if [[ -n "${FAKE_WP_FAIL:-}" && "$*" == *"$FAKE_WP_FAIL"* ]]; then exit 1; fi
config="$path/.wp-fake-config"
set_value() { grep -v "^$1=" "$config" > "$config.new" || true; echo "$1=$2" >> "$config.new"; mv "$config.new" "$config"; }
case "$1 $2" in
  "config get") grep "^$3=" "$config" | cut -d= -f2- ;;
  "config set")
    if [[ "$3" == "DB_PASSWORD" ]]; then read -r secret; set_value DB_PASSWORD "$secret"; else set_value "$3" "$4"; fi ;;
esac
if [[ "$*" == *"maintenance-mode deactivate"* ]]; then rm -f "$path/.maintenance"; fi
exit 0
`

func newPromotionFixture(t *testing.T) *promotionFixture {
	t.Helper()
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	log := filepath.Join(root, "helpers.log")
	t.Setenv("FAKE_LOG", log)
	writeExecutable := func(name, script string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	sites := map[string]struct{ content, database string }{
		"shop":     {"production", "spwp_shop"},
		"shop-stg": {"staging", "spwp_stg"},
	}
	for site, tree := range sites {
		public := filepath.Join(webRoot, "sites", site, "public")
		if err := os.MkdirAll(public, 0o750); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string]string{
			"wp-config.php":   "<?php // " + tree.content + "\n",
			"index.php":       tree.content,
			".wp-fake-config": "DB_NAME=" + tree.database + "\nDB_USER=wp_" + site + "\n",
		} {
			if err := os.WriteFile(filepath.Join(public, name), []byte(data), 0o640); err != nil {
				t.Fatal(err)
			}
		}
	}
	origin, _ := json.Marshal(stagingOrigin{ProductionSite: "shop", ProductionDomain: "shop.test", StagingDomain: "staging.shop.test", ProductionDB: "spwp_shop", StagingDB: "spwp_stg", StagingUser: "wp_stg"})
	if err := os.WriteFile(filepath.Join(webRoot, "sites", "shop-stg", stagingOriginMarker), origin, 0o600); err != nil {
		t.Fatal(err)
	}
	host := &fakePromotionHost{}
	app := &App{
		Config: Config{
			WebRoot:      webRoot,
			RecoveryRoot: filepath.Join(root, "recovery"),
			BackupRoot:   filepath.Join(root, "backups"),
			AuditLog:     filepath.Join(root, "audit.jsonl"),
			DBEngine:     "mysql",
			SiteCtl:      writeExecutable("fake-sitectl", "#!/usr/bin/env bash\necho \"sitectl $*\" >> \"$FAKE_LOG\"\n"),
			DBCtl: writeExecutable("fake-dbctl", "#!/usr/bin/env bash\necho \"dbctl $*\" >> \"$FAKE_LOG\"\n"+
				"[[ \"$1\" == inventory ]] && printf 'spwp_shop\\tshop\\twp_shop\\t1024\\tutf8mb4\\nspwp_stg\\tshop-stg\\twp_stg\\t1024\\tutf8mb4\\n'\nexit 0\n"),
			WPCLI: writeExecutable("fake-wp", fakeWPCLI),
		},
		Auth:                 Auth{Username: "admin", Secret: strings.Repeat("s", 32)},
		Routes:               &RouteStore{values: map[string]RouteDesired{"shop": {Name: "shop", Site: "shop", Domain: "shop.test", State: "applied"}, "shop-stg": {Name: "shop-stg", Site: "shop-stg", Domain: "staging.shop.test", State: "applied"}}},
		stagingPromotionHost: host,
	}
	return &promotionFixture{app: app, host: host, webRoot: webRoot, log: log}
}

func (f *promotionFixture) promote(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	asAdmin := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "admin"))
	}
	body := `{"source":"shop-stg","site":"shop"}`
	w := httptest.NewRecorder()
	f.app.stagingPromotionPreview(w, asAdmin(httptest.NewRequest(http.MethodPost, "/api/sites/staging/promote/preview", strings.NewReader(body))))
	if w.Code != http.StatusOK {
		t.Fatalf("preview: status %d: %s", w.Code, w.Body.String())
	}
	var preview ChangePreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || preview.PreviewToken == "" {
		t.Fatalf("preview response %q: %v", w.Body.String(), err)
	}
	w = httptest.NewRecorder()
	body = `{"source":"shop-stg","site":"shop","preview_token":"` + preview.PreviewToken + `"}`
	f.app.stagingPromote(w, asAdmin(httptest.NewRequest(http.MethodPost, "/api/sites/staging/promote", strings.NewReader(body))))
	return w
}

func (f *promotionFixture) production(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.webRoot, "sites", "shop", "public", name))
	if err != nil {
		t.Fatalf("read production %s: %v", name, err)
	}
	return string(data)
}

func (f *promotionFixture) helperCalls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// leftoverStages reports promotion stages that outlived the request.
func (f *promotionFixture) leftoverStages(t *testing.T) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(filepath.Join(f.webRoot, "sites"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".stepanel-promote-") {
			found = append(found, path)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestStagingPromotionReplacesProductionAndRecordsRecovery(t *testing.T) {
	f := newPromotionFixture(t)
	w := f.promote(t)
	if w.Code != http.StatusOK {
		t.Fatalf("promote: status %d: %s", w.Code, w.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	candidate, _ := result["database"].(string)
	if !strings.HasPrefix(candidate, "spwp_shop_") || result["previous_database_retained"] != "spwp_shop" {
		t.Fatalf("result = %v, want a new spwp_shop_* database retaining spwp_shop", result)
	}
	if got := f.production(t, "index.php"); got != "staging" {
		t.Fatalf("production index.php = %q, want the staged copy", got)
	}
	config := f.production(t, ".wp-fake-config")
	for _, want := range []string{"DB_NAME=" + candidate, "DB_USER=wp_shop_", "DB_HOST=localhost", "DB_PASSWORD="} {
		if !strings.Contains(config, want) {
			t.Fatalf("promoted config %q lacks %q", config, want)
		}
	}
	if _, err := os.Stat(filepath.Join(f.webRoot, "sites", "shop", "public", ".maintenance")); !os.IsNotExist(err) {
		t.Fatalf("maintenance gate left in place: %v", err)
	}
	if len(f.host.clones) != 1 || !strings.HasPrefix(f.host.clones[0], "spwp_stg->"+candidate+"/") {
		t.Fatalf("clones = %v, want spwp_stg cloned into %s", f.host.clones, candidate)
	}
	calls := f.helperCalls(t)
	for _, want := range []string{"sitectl seal shop", "search-replace https://staging.shop.test https://shop.test", "option update home https://shop.test", "db check"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("helper calls lack %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "drop-managed") {
		t.Fatalf("successful promotion dropped a database:\n%s", calls)
	}
	record, err := os.ReadFile(filepath.Join(f.webRoot, "sites", "shop", wordpressPromotionRecordName))
	if err != nil || !strings.Contains(string(record), `"previous_production_database":"spwp_shop"`) || !strings.Contains(string(record), `"promoted_database":"`+candidate+`"`) {
		t.Fatalf("promotion record = %q, %v", record, err)
	}
	if pending := pendingTransactions(t, f.app.Config.RecoveryRoot); len(pending) != 0 {
		t.Fatalf("pending transactions after success: %v", pending)
	}
	if stages := f.leftoverStages(t); len(stages) != 0 {
		t.Fatalf("promotion stages left behind: %v", stages)
	}
}

// TestStagingPromotionFailureRestoresProduction fails each promotion step and
// requires production files, configuration, and recovery state to be exactly
// as before, with any candidate database dropped.
func TestStagingPromotionFailureRestoresProduction(t *testing.T) {
	for _, test := range []struct {
		name       string
		failAt     string // STEPANEL_FAIL_AT point
		wpFail     string // FAKE_WP_FAIL argv substring
		failBackup bool
		failClone  bool
		cloneTried bool
	}{
		{name: "safety backup", failBackup: true},
		{name: "after backup", failAt: "backup"},
		{name: "file copy", failAt: "copy"},
		{name: "database clone", failClone: true, cloneTried: true},
		{name: "after clone", failAt: "clone", cloneTried: true},
		{name: "activation", failAt: "activate", cloneTried: true},
		{name: "seal", failAt: "seal", cloneTried: true},
		{name: "configure", failAt: "configure", cloneTried: true},
		{name: "config set", wpFail: "config set DB_USER", cloneTried: true},
		{name: "search-replace", wpFail: "search-replace", cloneTried: true},
		{name: "database check", wpFail: "db check", cloneTried: true},
		{name: "promotion record", failAt: "record", cloneTried: true},
		{name: "maintenance gate", wpFail: "maintenance-mode deactivate", cloneTried: true},
		{name: "commit", failAt: "commit", cloneTried: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPromotionFixture(t)
			f.host.failBackup, f.host.failClone = test.failBackup, test.failClone
			if test.failAt != "" {
				t.Setenv("STEPANEL_FAIL_AT", "promote:"+test.failAt)
			}
			t.Setenv("FAKE_WP_FAIL", test.wpFail)
			w := f.promote(t)
			if w.Code == http.StatusOK {
				t.Fatalf("promotion succeeded despite a %s failure", test.name)
			}
			if got := f.production(t, "index.php"); got != "production" {
				t.Fatalf("production index.php = %q after failed promotion", got)
			}
			if got := f.production(t, ".wp-fake-config"); got != "DB_NAME=spwp_shop\nDB_USER=wp_shop\n" {
				t.Fatalf("production WordPress config = %q after failed promotion", got)
			}
			if _, err := os.Stat(filepath.Join(f.webRoot, "sites", "shop", wordpressPromotionRecordName)); !os.IsNotExist(err) {
				t.Fatalf("promotion record exists after failure: %v", err)
			}
			if pending := pendingTransactions(t, f.app.Config.RecoveryRoot); len(pending) != 0 {
				t.Fatalf("failed promotion left pending transactions %v", pending)
			}
			if stages := f.leftoverStages(t); len(stages) != 0 {
				t.Fatalf("failed promotion left stages: %v", stages)
			}
			calls := f.helperCalls(t)
			dropped := strings.Contains(calls, "dbctl drop-managed spwp_shop_")
			if dropped != test.cloneTried {
				t.Fatalf("candidate database dropped = %v, want %v:\n%s", dropped, test.cloneTried, calls)
			}
			if strings.Contains(calls, "drop-managed spwp_shop ") || strings.Contains(calls, "drop-managed spwp_stg") {
				t.Fatalf("failed promotion dropped an existing database:\n%s", calls)
			}
		})
	}
}
