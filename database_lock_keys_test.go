package stepanel

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/operations"
)

// TestDatabaseLocksUseOneKeyHelper keeps every database lock on one key
// format. A handler that spelled the key differently (or locked the site name
// instead) would silently stop excluding drops and credential rotations.
func TestDatabaseLocksUseOneKeyHelper(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, `"database:"`) && !(file == "site_lock.go" && strings.Contains(line, `return "database:" + name`)) {
				t.Errorf("%s:%d builds a database lock key by hand; use databaseLockKey", file, i+1)
			}
		}
	}
}

func TestStagingPromotionLocksBothSitesAndDatabases(t *testing.T) {
	state := promotionBefore{Source: "shop-stg", Production: "shop", StagingDatabase: "spwp_shop_stg", ActiveDatabase: "spwp_shop"}
	keys := stagingPromotionLockKeys(state)
	for _, want := range []string{"shop", "shop-stg", databaseLockKey("spwp_shop_stg"), databaseLockKey("spwp_shop")} {
		if !slices.Contains(keys, want) {
			t.Fatalf("promotion lock keys %q omit %q", keys, want)
		}
	}
}

// TestStagingCloneWaitsForSourceDatabaseLock runs the clone handler in one
// "process" while another holds the source database's lock, as a database
// drop or credential rotation would.
func TestStagingCloneWaitsForSourceDatabaseLock(t *testing.T) {
	root := t.TempDir()
	dsn := "file:" + filepath.Join(root, "control-plane.sqlite") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	openLocks := func(owner string) *operations.DBLocks {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		locks, err := operations.NewDBLocks(db, owner, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return locks
	}
	rotation := &App{dbLocks: openLocks("rotation"), Config: Config{WebRoot: root}}
	clone := &App{dbLocks: openLocks("clone"), Config: Config{WebRoot: root}}

	_, release, err := rotation.acquireSiteMutationLockContext(context.Background(), databaseLockKey("shop_db"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	body := `{"source":"shop","site":"shopstg","domain":"staging.shop.test","database":true,"source_database":"shop_db","target_database":"shopstg_db","target_user":"shopstg_user","target_password":"0123456789abcdefghijKLMN"}`
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/api/staging", strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	clone.stagingCreate(w, r)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "busy") {
		t.Fatalf("clone while the source database was locked: status %d %q, want 409 busy", w.Code, w.Body.String())
	}
}
