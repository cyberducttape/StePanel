package rootbroker

import (
	"context"
	"io"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/cyberducttape/StePanel/internal/siteidentity"
)

// fakeHost records privileged host mutations instead of running them.
type fakeHost struct {
	mu       sync.Mutex
	users    []string
	deleted  []string
	chowns   []string
	webGroup string
}

func (f *fakeHost) EnsureSystemUser(_ context.Context, username, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users = append(f.users, username)
	return nil
}

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
	if len(host.users) != 2 || len(host.deleted) != 1 {
		t.Fatalf("users created = %v, deleted = %v", host.users, host.deleted)
	}
	for _, chown := range host.chowns {
		if !strings.HasPrefix(chown, want+":apache ") {
			t.Fatalf("chown %q does not use the shared identity and detected web group", chown)
		}
	}
	if len(host.chowns) != 4 {
		t.Fatalf("chowns = %v, want three PHP state directories plus the created site root", host.chowns)
	}
}
