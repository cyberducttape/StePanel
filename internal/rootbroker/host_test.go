package rootbroker

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/siteidentity"
)

// fakeHost records privileged host mutations instead of running them.
type fakeHost struct {
	mu       sync.Mutex
	users    []string
	deleted  []string
	chowns   []string
	helper   [][]string
	webGroup string
	// helperUser overrides the account the fake site helper reports.
	helperUser string
	// helperStarted/helperRelease make the helper concurrency test deterministic.
	helperStarted chan struct{}
	helperEntered chan string
	helperRelease <-chan struct{}
	activeHelpers int
	maxActive     int
}

func (f *fakeHost) RunSiteHelper(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.helper = append(f.helper, args)
	if f.helperStarted != nil {
		select {
		case <-f.helperStarted:
		default:
			close(f.helperStarted)
		}
	}
	f.activeHelpers++
	if f.activeHelpers > f.maxActive {
		f.maxActive = f.activeHelpers
	}
	release := f.helperRelease
	f.mu.Unlock()
	if f.helperEntered != nil {
		f.helperEntered <- args[1]
	}
	if release != nil {
		<-release
	}
	f.mu.Lock()
	f.activeHelpers--
	f.mu.Unlock()
	if f.helperUser != "" {
		return f.helperUser + "\n", nil
	}
	return args[2] + "\n", nil
}

func (f *fakeHost) RunSiteHelperInput(ctx context.Context, _ []byte, args ...string) (string, error) {
	return f.RunSiteHelper(ctx, args...)
}

func (f *fakeHost) EnsureSystemUser(_ context.Context, username, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users = append(f.users, username)
	return nil
}

func (f *fakeHost) ValidateSystemUser(_ context.Context, _ string, _ string) error { return nil }

func (f *fakeHost) DeleteSystemUser(_ context.Context, username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, username)
	return nil
}

func (f *fakeHost) Chown(_ context.Context, path, owner, group string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chowns = append(f.chowns, owner+":"+group+" "+path)
	return nil
}

func (f *fakeHost) WebGroup() (string, error) {
	if f.webGroup == "" {
		return "www-data", nil
	}
	return f.webGroup, nil
}

// newTestBroker builds a broker with a temporary recovery root and a fake
// host so tests never mutate real accounts or ownership.
func newTestBroker(t *testing.T, webRoot string, logger *log.Logger) (*Broker, error) {
	t.Helper()
	return newBroker(webRoot, t.TempDir(), logger, &fakeHost{})
}

func TestTemporaryWebRootDoesNotEnableTestBehavior(t *testing.T) {
	// A production config may legitimately place the web root under the
	// temporary directory; that must not relax the durable-journal
	// requirement or skip privileged host mutations.
	_, err := NewBrokerWithRecoveryRoot(t.TempDir(), "/proc/stepanel-recovery", log.New(io.Discard, "", 0))
	if err == nil || !strings.Contains(err.Error(), "durable recovery root") {
		t.Fatalf("NewBrokerWithRecoveryRoot error = %v, want durable recovery root failure", err)
	}
	broker, err := NewBrokerWithRecoveryRoot(t.TempDir(), t.TempDir(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := broker.host.(execHostOps); !ok {
		t.Fatalf("public constructor host = %T, want execHostOps", broker.host)
	}
}

func TestBrokerSiteSnapshotRenamesOnlyIntoRecoveryRoot(t *testing.T) {
	webRoot := t.TempDir()
	recoveryRoot := t.TempDir()
	sitePublic := filepath.Join(webRoot, "sites", "demo", "public")
	if err := os.MkdirAll(sitePublic, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sitePublic, "index.html"), []byte("live"), 0640); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(recoveryRoot, "txn", "site-before")
	if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		t.Fatal(err)
	}
	broker, err := newBroker(webRoot, recoveryRoot, log.New(io.Discard, "", 0), &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	if response, err := broker.siteSnapshot(context.Background(), &SiteRequest{Action: "snapshot", Site: "demo", BackupPath: backup}); err != nil || !response.OK {
		t.Fatalf("siteSnapshot = %#v, %v", response, err)
	}
	if _, err := os.Stat(filepath.Join(backup, "index.html")); err != nil {
		t.Fatalf("snapshot content missing: %v", err)
	}
	if _, err := os.Stat(sitePublic); !os.IsNotExist(err) {
		t.Fatalf("live public tree still exists, stat error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if response, err := broker.siteSnapshot(context.Background(), &SiteRequest{Action: "snapshot", Site: "demo", BackupPath: outside}); err != nil || response.OK {
		t.Fatalf("outside snapshot = %#v, %v; want rejection", response, err)
	}
}

func TestSiteLifecycleUsesSharedIdentityAndWebGroup(t *testing.T) {
	webRoot := t.TempDir()
	host := &fakeHost{webGroup: "apache"}
	broker, err := newBroker(webRoot, t.TempDir(), log.New(io.Discard, "", 0), host)
	if err != nil {
		t.Fatal(err)
	}
	const site = "my_long_site_name_over_18"
	want := siteidentity.UnixUser(site)
	for _, action := range []string{"prepare", "create", "delete"} {
		resp, err := broker.Execute(context.Background(), &Request{RequestType: "site", Site: &SiteRequest{Action: action, Site: site}})
		if err != nil || !resp.OK {
			t.Fatalf("%s response = %#v, err = %v", action, resp, err)
		}
	}
	for _, user := range append(append([]string{}, host.users...), host.deleted...) {
		if user != want {
			t.Fatalf("broker used site user %q, want shared identity %q", user, want)
		}
	}
	if len(host.users) != 1 || len(host.deleted) != 1 {
		t.Fatalf("users created = %v, deleted = %v", host.users, host.deleted)
	}
	if len(host.chowns) != 1 || !strings.HasPrefix(host.chowns[0], want+":apache ") {
		t.Fatalf("chowns = %v, want the created site root owned by the shared identity and detected web group", host.chowns)
	}
	if len(host.helper) != 1 || strings.Join(host.helper[0], " ") != "prepare-root "+site+" "+want {
		t.Fatalf("site helper calls = %v, want prepare delegated to prepare-root", host.helper)
	}
}

func TestSitePrepareRefusesDivergentHelperIdentity(t *testing.T) {
	host := &fakeHost{helperUser: "sp-legacy-name"}
	broker, err := newBroker(t.TempDir(), t.TempDir(), log.New(io.Discard, "", 0), host)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := broker.Execute(context.Background(), &Request{RequestType: "site", Site: &SiteRequest{Action: "prepare", Site: "demo"}})
	if err != nil || resp.OK || !strings.Contains(resp.Error, "divergent site identity") {
		t.Fatalf("prepare response = %#v, err = %v; want divergent identity refusal", resp, err)
	}
}

func TestSiteSealUsesPersistedIdentityHelper(t *testing.T) {
	webRoot := t.TempDir()
	public := filepath.Join(webRoot, "sites", "demo", "public")
	if err := os.MkdirAll(public, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.php"), []byte("<?php"), 0o640); err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{}
	broker, err := newBroker(webRoot, t.TempDir(), log.New(io.Discard, "", 0), host)
	if err != nil {
		t.Fatal(err)
	}
	const site = "demo"
	want := siteidentity.UnixUser(site)
	resp, err := broker.Execute(context.Background(), &Request{RequestType: "site", Site: &SiteRequest{Action: "seal", Site: site}})
	if err != nil || !resp.OK {
		t.Fatalf("seal response = %#v, err = %v", resp, err)
	}
	if len(host.helper) != 1 || strings.Join(host.helper[0], " ") != "seal "+site+" "+want {
		t.Fatalf("site helper calls = %v, want seal delegated with persisted identity", host.helper)
	}
}

func TestSiteSealRefusesDivergentHelperIdentity(t *testing.T) {
	host := &fakeHost{helperUser: "sp-legacy-name"}
	broker, err := newBroker(t.TempDir(), t.TempDir(), log.New(io.Discard, "", 0), host)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := broker.Execute(context.Background(), &Request{RequestType: "site", Site: &SiteRequest{Action: "seal", Site: "demo"}})
	if err != nil || resp.OK || !strings.Contains(resp.Error, "divergent site identity") {
		t.Fatalf("seal response = %#v, err = %v; want divergent identity refusal", resp, err)
	}
}

func TestSiteAccountMutationsAreSerializedAcrossBrokerRequests(t *testing.T) {
	release := make(chan struct{})
	host := &fakeHost{helperStarted: make(chan struct{}), helperEntered: make(chan string, 2), helperRelease: release}
	broker, err := newBroker(t.TempDir(), t.TempDir(), log.New(io.Discard, "", 0), host)
	if err != nil {
		t.Fatal(err)
	}
	request := func(site string) *Request {
		return &Request{RequestType: "site", Site: &SiteRequest{Action: "prepare", Site: site}}
	}

	firstDone := make(chan struct{})
	go func() {
		_, _ = broker.Execute(context.Background(), request("first"))
		close(firstDone)
	}()
	<-host.helperStarted
	if site := <-host.helperEntered; site != "first" {
		t.Fatalf("first helper entered for site %q", site)
	}

	secondDone := make(chan struct{})
	go func() {
		_, _ = broker.Execute(context.Background(), request("second"))
		close(secondDone)
	}()
	select {
	case site := <-host.helperEntered:
		t.Fatalf("second account mutation entered host helper for %q before the first was released", site)
	case <-time.After(50 * time.Millisecond):
	}

	host.mu.Lock()
	maxActive := host.maxActive
	host.mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("concurrent account mutations reached host helper: max active = %d, want 1", maxActive)
	}
	close(release)
	<-firstDone
	<-secondDone
}

// Every stepanel-sitectl action runs ensure_site_user (useradd/usermod), so
// the configuration actions must share the account mutation lock with
// prepare even though they target different sites.
func TestSiteHelperConfigActionsShareAccountMutationLock(t *testing.T) {
	enabled := true
	actions := map[string]*SiteRequest{
		"access":      {Action: "access", SFTPEnabled: &enabled, ShellEnabled: &enabled},
		"ftp":         {Action: "ftp", FTPEnabled: &enabled, FTPPassword: "test-ftps-password-123"},
		"resources":   {Action: "resources", PHPWorkers: 4},
		"quota":       {Action: "quota", DiskMB: 1024, Inodes: 100000},
		"quota-clear": {Action: "quota-clear"},
		"runtime": {Action: "runtime", PHPVersion: "8.3", MemoryLimit: "256M", ExecTimeout: 30,
			UploadMaxFilesize: "64M", PostMaxSize: "64M", MaxInputVars: 1000, ErrorReporting: "E_ALL"},
	}
	for name, second := range actions {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			host := &fakeHost{helperStarted: make(chan struct{}), helperEntered: make(chan string, 2), helperRelease: release}
			broker, err := newBroker(t.TempDir(), t.TempDir(), log.New(io.Discard, "", 0), host)
			if err != nil {
				t.Fatal(err)
			}
			firstDone := make(chan struct{})
			go func() {
				_, _ = broker.Execute(context.Background(), &Request{RequestType: "site", Site: &SiteRequest{Action: "prepare", Site: "first"}})
				close(firstDone)
			}()
			<-host.helperStarted
			<-host.helperEntered

			second.Site = "second"
			secondDone := make(chan struct{})
			go func() {
				_, _ = broker.Execute(context.Background(), &Request{RequestType: "site", Site: second})
				close(secondDone)
			}()
			select {
			case site := <-host.helperEntered:
				t.Fatalf("%s for %q entered the host helper while an account mutation was running", name, site)
			case <-time.After(50 * time.Millisecond):
			}
			close(release)
			<-firstDone
			<-secondDone
		})
	}
}
