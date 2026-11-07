package stepanel

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
	"golang.org/x/sys/unix"
)

func TestReleaseActivationRecoversAfterProcessKill(t *testing.T) {
	if os.Getenv("STEPANEL_RELEASE_ACTIVATION_CHILD") == "1" {
		root := os.Getenv("STEPANEL_RELEASE_ACTIVATION_ROOT")
		cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
		siteRoot := filepath.Join(cfg.WebRoot, "sites", "example")
		if err := os.MkdirAll(filepath.Join(siteRoot, "public"), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(siteRoot, "public", "index.html"), []byte("old"), 0640); err != nil {
			t.Fatal(err)
		}
		manager, err := newSiteManagerForConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		staged, owner, err := manager.CreateReleaseStagingLease(context.Background(), "example", ".stepanel-release-crash-")
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Close()
		if err := os.WriteFile(filepath.Join(staged, "index.html"), []byte("new"), 0640); err != nil {
			t.Fatal(err)
		}
		journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", staged)
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.persist(); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ActivateStagedReplacing(context.Background(), "example", staged); err != nil {
			t.Fatal(err)
		}
		// The process dies after publication but before markActivated. Recovery
		// must use the prepared journal plus the manager-owned previous tree to
		// restore the old release rather than leaving the new tree live.
		if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		return
	}

	root := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=TestReleaseActivationRecoversAfterProcessKill", "-test.v")
	child.Env = append(os.Environ(),
		"STEPANEL_RELEASE_ACTIVATION_CHILD=1",
		"STEPANEL_RELEASE_ACTIVATION_ROOT="+root,
	)
	output, err := child.CombinedOutput()
	if err == nil {
		t.Fatal("child process unexpectedly survived the simulated crash")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("child exit = %v, want SIGKILL", err)
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child exit = %v, want SIGKILL; output: %s", err, output)
	}

	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	if _, err := recoverReleaseActivationJournals(cfg); err != nil {
		t.Fatalf("recover release activation: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.WebRoot, "sites", "example", "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("active release after recovery = %q, want old", data)
	}
}

func TestRecoverReleaseActivationJournalRollsBackActivatedSwap(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	siteRoot := filepath.Join(cfg.WebRoot, "sites", "example")
	public := filepath.Join(siteRoot, "public")
	previous := filepath.Join(siteRoot, ".stepanel-previous-1")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previous, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.html"), []byte("new"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "index.html"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}

	journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", filepath.Join(siteRoot, ".stepanel-release-next"))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.persist(); err != nil {
		t.Fatal(err)
	}
	if err := journal.markActivated(previous); err != nil {
		t.Fatal(err)
	}

	ids, err := recoverReleaseActivationJournals(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != journal.ID {
		t.Fatalf("recovered IDs = %v, want %q", ids, journal.ID)
	}
	data, err := os.ReadFile(filepath.Join(public, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("active release = %q, want old", data)
	}
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Fatalf("previous release still exists: %v", err)
	}
	if _, err := os.Stat(journal.path); !os.IsNotExist(err) {
		t.Fatalf("journal still exists: %v", err)
	}
}

func TestRecoverReleaseActivationJournalRollsBackPreparedSwapAfterFirstRename(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	siteRoot := filepath.Join(cfg.WebRoot, "sites", "example")
	previous := filepath.Join(siteRoot, ".stepanel-previous-2")
	stage := filepath.Join(siteRoot, ".stepanel-release-next")
	for _, path := range []string{previous, stage} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(previous, "index.html"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "index.html"), []byte("new"), 0640); err != nil {
		t.Fatal(err)
	}

	journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.persist(); err != nil {
		t.Fatal(err)
	}
	ids, err := recoverReleaseActivationJournals(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != journal.ID {
		t.Fatalf("recovered IDs = %v, want %q", ids, journal.ID)
	}
	data, err := os.ReadFile(filepath.Join(siteRoot, "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("restored release = %q, want old", data)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("interrupted release stage still exists: %v", err)
	}
}

func TestRecoverPreparedReleaseDoesNotRollbackAnUnstartedSwap(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	siteRoot := filepath.Join(cfg.WebRoot, "sites", "example")
	public := filepath.Join(siteRoot, "public")
	previous := filepath.Join(siteRoot, ".stepanel-previous-old")
	stage := filepath.Join(siteRoot, ".stepanel-release-next")
	for _, path := range []string{public, previous, stage} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(public, "index.html"), []byte("current"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "index.html"), []byte("older"), 0640); err != nil {
		t.Fatal(err)
	}

	journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.persist(); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverReleaseActivationJournals(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(public, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "current" {
		t.Fatalf("unstarted activation changed active release to %q", data)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staged release was not discarded: %v", err)
	}
}

func TestReleaseStageExistsBindsManagerStagesToTheirSite(t *testing.T) {
	webRoot := t.TempDir()
	stageRoot := filepath.Join(webRoot, "sites", ".stepanel-manager-staging")
	own := filepath.Join(stageRoot, ".stepanel-release-blog-123")
	other := filepath.Join(stageRoot, ".stepanel-release-blog-2-123")
	for _, dir := range []string{filepath.Join(webRoot, "sites", "blog"), own, other} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if exists, err := releaseStageExists(Config{WebRoot: webRoot}, "blog", own); err != nil || !exists {
		t.Fatalf("own stage exists = %v, %v", exists, err)
	}
	if _, err := releaseStageExists(Config{WebRoot: webRoot}, "blog", other); err == nil {
		t.Fatal("site blog accepted a release stage of site blog-2")
	}
}

// A site placed in a hosting account stages releases on the home filesystem
// (ADR-0007); startup recovery must recognise those stages and nothing else.
func TestReleaseStageExistsAcceptsAPlacedSitesHomeStage(t *testing.T) {
	cfg := placedSiteConfig(t)
	homeStages := filepath.Join(cfg.HomeRoot, ".stepanel-manager-staging")
	own := filepath.Join(homeStages, ".stepanel-release-shop-123")
	legacyRoot := filepath.Join(cfg.WebRoot, "sites", ".stepanel-manager-staging", ".stepanel-release-shop-456")
	for _, dir := range []string{own, legacyRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if exists, err := releaseStageExists(cfg, "shop", own); err != nil || !exists {
		t.Fatalf("placed home stage exists = %v, %v", exists, err)
	}
	if _, err := releaseStageExists(cfg, "shop", legacyRoot); err == nil {
		t.Fatal("a placed site accepted a stage from the legacy staging root")
	}
	if _, err := releaseStageExists(cfg, "alice", own); err == nil {
		t.Fatal("site alice accepted a release stage of site shop")
	}
}

// A crash after the activation exchanged the stage with the live release,
// but before it moved the old release aside, leaves the old release at the
// stage path and the uncommitted one live. Recovery recognises the stage
// path no longer holds the journal's stage and restores the old release.
func TestRecoverReleaseActivationAfterExchangeBeforeMoveAside(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	public := filepath.Join(cfg.WebRoot, "sites", "example", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.html"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg}
	stage, owner, err := app.createSiteReleaseStaging(context.Background(), "example", ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := os.WriteFile(filepath.Join(stage, "index.html"), []byte("new"), 0640); err != nil {
		t.Fatal(err)
	}
	journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.persist(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, public, unix.RENAME_EXCHANGE); err != nil {
		t.Skipf("filesystem cannot exchange: %v", err)
	}
	ids, err := recoverReleaseActivationJournals(cfg)
	if err != nil || len(ids) != 1 {
		t.Fatalf("recovered %v, %v", ids, err)
	}
	if data, err := os.ReadFile(filepath.Join(public, "index.html")); err != nil || string(data) != "old" {
		t.Fatalf("active release after recovery = %q, %v; want the old release", data, err)
	}
	if _, err := os.Lstat(stage); !os.IsNotExist(err) {
		t.Fatalf("stage path still holds a tree: %v", err)
	}
}

// indeterminateManager performs the exchange half of an activation and then
// reports that it could not tell what it left, as when undoing a failed
// move-aside also fails.
type indeterminateManager struct {
	*siteauthority.DefaultManager
	public string
}

func (m indeterminateManager) ActivateStagedReplacing(ctx context.Context, name, stagedRoot string) (string, error) {
	if err := unix.Renameat2(unix.AT_FDCWD, stagedRoot, unix.AT_FDCWD, m.public, unix.RENAME_EXCHANGE); err != nil {
		return "", err
	}
	return "", errors.Join(siteauthority.ErrActivationIndeterminate, errors.New("injected: reverse exchange failed"))
}

func newIndeterminateActivationFixture(t *testing.T) (*App, string, string) {
	t.Helper()
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	public := filepath.Join(cfg.WebRoot, "sites", "example", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.html"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	manager, err := newSiteManagerForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg, siteManager: indeterminateManager{manager, public}}
	stage, owner, err := app.createSiteReleaseStaging(context.Background(), "example", ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close() })
	if err := os.WriteFile(filepath.Join(stage, "index.html"), []byte("new"), 0640); err != nil {
		t.Fatal(err)
	}
	return app, public, stage
}

// An activation that ends in an indeterminate state keeps its journal and
// reconciles at once: the previous release, left at the stage path, is put
// back instead of being discarded with the stage.
func TestIndeterminateActivationIsReconciledNotDiscarded(t *testing.T) {
	app, public, stage := newIndeterminateActivationFixture(t)
	// Exchanging twice leaves the trees where they were and shows the
	// filesystem supports the exchange the fake manager performs.
	for i := 0; i < 2; i++ {
		if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, public, unix.RENAME_EXCHANGE); err != nil {
			t.Skipf("filesystem cannot exchange: %v", err)
		}
	}
	_, err := app.activatePipelineRelease(context.Background(), "example", stage)
	if err == nil || !errors.Is(err, siteauthority.ErrActivationIndeterminate) {
		t.Fatalf("activation error = %v, want the indeterminate failure", err)
	}
	if data, err := os.ReadFile(filepath.Join(public, "index.html")); err != nil || string(data) != "old" {
		t.Fatalf("live release after reconciliation = %q, %v; want old", data, err)
	}
	if entries, _ := os.ReadDir(app.Config.RecoveryRoot); len(entries) != 0 {
		t.Fatalf("reconciled activation left %d journal entries", len(entries))
	}
}

// A stage an activation journal still names is not discarded: it may hold
// the previous release.
func TestJournaledStageIsNotDiscarded(t *testing.T) {
	app, _, stage := newIndeterminateActivationFixture(t)
	journal, err := newReleaseActivationJournal(app.Config.RecoveryRoot, "example", stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.persist(); err != nil {
		t.Fatal(err)
	}
	if err := app.discardSiteReleaseStaging(context.Background(), "example", stage); err == nil {
		t.Fatal("a journaled stage was discarded")
	}
	if _, err := os.Stat(filepath.Join(stage, "index.html")); err != nil {
		t.Fatalf("journaled stage was removed: %v", err)
	}
}

// Recovery restores the release the activation moved aside, identified by
// the document root's inode, not the newest retained release by name.
func TestRecoveryRestoresTheMovedAsideReleaseByIdentity(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	siteRoot := filepath.Join(cfg.WebRoot, "sites", "example")
	public := filepath.Join(siteRoot, "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.html"), []byte("live"), 0640); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(siteRoot, ".stepanel-previous-9999999999999999999")
	if err := os.MkdirAll(newer, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newer, "index.html"), []byte("older release, newer name"), 0640); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg}
	stage, owner, err := app.createSiteReleaseStaging(context.Background(), "example", ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.recordLive(public); err != nil {
		t.Fatal(err)
	}
	if err := journal.persist(); err != nil {
		t.Fatal(err)
	}
	// What the two-rename fallback leaves after a crash between its renames.
	if err := os.Rename(public, filepath.Join(siteRoot, ".stepanel-previous-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverReleaseActivationJournals(cfg); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(public, "index.html")); err != nil || string(data) != "live" {
		t.Fatalf("recovered live release = %q, %v; want the moved-aside one", data, err)
	}
	if _, err := os.Stat(newer); err != nil {
		t.Fatalf("an unrelated retained release was consumed: %v", err)
	}
}
