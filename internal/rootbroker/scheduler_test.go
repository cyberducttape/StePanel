package rootbroker

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingExecutor records which requests started and holds each one until
// the test releases it by key.
type blockingExecutor struct {
	mu       sync.Mutex
	started  chan string
	releases map[string]chan struct{}
	deadline map[string]time.Duration
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{started: make(chan string, 16), releases: make(map[string]chan struct{}), deadline: make(map[string]time.Duration)}
}

func (e *blockingExecutor) release(name string) {
	close(e.gate(name))
}

func (e *blockingExecutor) gate(name string) chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.releases[name]; !ok {
		e.releases[name] = make(chan struct{})
	}
	return e.releases[name]
}

func (e *blockingExecutor) execute(ctx context.Context, req *Request) (*Response, error) {
	name := testRequestName(req)
	if deadline, ok := ctx.Deadline(); ok {
		e.mu.Lock()
		e.deadline[name] = time.Until(deadline)
		e.mu.Unlock()
	}
	e.started <- name
	if req.RequestType == "health" {
		return &Response{OK: true}, nil
	}
	<-e.gate(name)
	return &Response{OK: true}, nil
}

// testRequestName identifies a test request by its site or helper name.
func testRequestName(req *Request) string {
	switch {
	case req.RequestType == "health":
		return "health"
	case req.App != nil:
		return req.App.Site + "/" + req.App.Action
	case req.Proxy != nil:
		return "proxy"
	}
	return req.RequestType
}

func appRequest(site, action string) *Request {
	return &Request{RequestType: "app", App: &AppRequest{Action: action, Site: site}}
}

func newTestScheduler(t *testing.T, limit int) (*Scheduler, *blockingExecutor) {
	t.Helper()
	exec := newBlockingExecutor()
	scheduler, err := newScheduler(exec.execute, func(*Request) error { return nil }, limit)
	if err != nil {
		t.Fatal(err)
	}
	return scheduler, exec
}

func runAsync(scheduler *Scheduler, ctx context.Context, req *Request) <-chan *Response {
	done := make(chan *Response, 1)
	go func() {
		resp, _ := scheduler.Execute(ctx, req)
		done <- resp
	}()
	return done
}

func expectStarted(t *testing.T, exec *blockingExecutor, want string) {
	t.Helper()
	select {
	case got := <-exec.started:
		if got != want {
			t.Fatalf("started %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%q did not start", want)
	}
}

func expectNotStarted(t *testing.T, exec *blockingExecutor) {
	t.Helper()
	select {
	case got := <-exec.started:
		t.Fatalf("%q started while it should be waiting", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// waitForExclusiveWaiter blocks until an exclusive request is queued at the
// gate, so tests can order a following shared request deterministically.
func waitForExclusiveWaiter(t *testing.T, scheduler *Scheduler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		scheduler.gate.mu.Lock()
		waiting := scheduler.gate.waitingExclusive
		scheduler.gate.mu.Unlock()
		if waiting > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("exclusive request never queued")
}

func TestSchedulerRunsUnrelatedSitesConcurrently(t *testing.T) {
	scheduler, exec := newTestScheduler(t, 4)
	ctx := context.Background()
	first := runAsync(scheduler, ctx, appRequest("alpha", "apply"))
	expectStarted(t, exec, "alpha/apply")
	second := runAsync(scheduler, ctx, appRequest("beta", "restart"))
	expectStarted(t, exec, "beta/restart")
	exec.release("beta/restart")
	if resp := <-second; !resp.OK {
		t.Fatalf("beta response = %+v", resp)
	}
	exec.release("alpha/apply")
	<-first
}

func TestSchedulerSerializesSameSite(t *testing.T) {
	scheduler, exec := newTestScheduler(t, 4)
	ctx := context.Background()
	first := runAsync(scheduler, ctx, appRequest("alpha", "apply"))
	expectStarted(t, exec, "alpha/apply")
	second := runAsync(scheduler, ctx, appRequest("alpha", "restart"))
	expectNotStarted(t, exec)
	exec.release("alpha/apply")
	<-first
	expectStarted(t, exec, "alpha/restart")
	exec.release("alpha/restart")
	<-second
}

func TestSchedulerBoundsConcurrentExecution(t *testing.T) {
	scheduler, exec := newTestScheduler(t, 1)
	ctx := context.Background()
	first := runAsync(scheduler, ctx, appRequest("alpha", "apply"))
	expectStarted(t, exec, "alpha/apply")
	second := runAsync(scheduler, ctx, appRequest("beta", "apply"))
	expectNotStarted(t, exec)
	exec.release("alpha/apply")
	<-first
	expectStarted(t, exec, "beta/apply")
	exec.release("beta/apply")
	<-second
}

func TestSchedulerHealthBypassesBusyBroker(t *testing.T) {
	scheduler, exec := newTestScheduler(t, 1)
	ctx := context.Background()
	busy := runAsync(scheduler, ctx, appRequest("alpha", "apply"))
	expectStarted(t, exec, "alpha/apply")
	healthCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	resp, err := scheduler.Execute(healthCtx, &Request{RequestType: "health"})
	if err != nil || !resp.OK {
		t.Fatalf("health while busy = %+v, %v", resp, err)
	}
	expectStarted(t, exec, "health")
	exec.release("alpha/apply")
	<-busy
}

func TestSchedulerUnscopedRequestRunsExclusively(t *testing.T) {
	scheduler, exec := newTestScheduler(t, 4)
	ctx := context.Background()
	running := runAsync(scheduler, ctx, appRequest("alpha", "apply"))
	expectStarted(t, exec, "alpha/apply")
	// Proxy requests carry no resource, so they must wait for all running
	// operations and hold back new ones.
	exclusive := runAsync(scheduler, ctx, &Request{RequestType: "proxy", Proxy: &ProxyRequest{Action: "reload"}})
	waitForExclusiveWaiter(t, scheduler)
	expectNotStarted(t, exec)
	queued := runAsync(scheduler, ctx, appRequest("beta", "apply"))
	expectNotStarted(t, exec)

	exec.release("alpha/apply")
	<-running
	expectStarted(t, exec, "proxy")
	expectNotStarted(t, exec)
	exec.release("proxy")
	<-exclusive
	expectStarted(t, exec, "beta/apply")
	exec.release("beta/apply")
	<-queued
}

func TestSchedulerWaitIsBoundedByContext(t *testing.T) {
	scheduler, exec := newTestScheduler(t, 4)
	running := runAsync(scheduler, context.Background(), appRequest("alpha", "apply"))
	expectStarted(t, exec, "alpha/apply")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	resp, err := scheduler.Execute(ctx, appRequest("alpha", "restart"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, "root broker is busy") || !strings.Contains(resp.Error, "site:alpha") {
		t.Fatalf("queued response = %+v, want busy error naming the resource", resp)
	}
	expectNotStarted(t, exec)

	// The abandoned waiter must not leave the resource locked.
	exec.release("alpha/apply")
	<-running
	next := runAsync(scheduler, context.Background(), appRequest("alpha", "stop"))
	expectStarted(t, exec, "alpha/stop")
	exec.release("alpha/stop")
	<-next
}

func TestSchedulerAbandonedExclusiveWaiterUnblocksSharedRequests(t *testing.T) {
	scheduler, exec := newTestScheduler(t, 4)
	running := runAsync(scheduler, context.Background(), appRequest("alpha", "apply"))
	expectStarted(t, exec, "alpha/apply")
	ctx, cancel := context.WithCancel(context.Background())
	exclusive := runAsync(scheduler, ctx, &Request{RequestType: "proxy", Proxy: &ProxyRequest{Action: "reload"}})
	waitForExclusiveWaiter(t, scheduler)
	queued := runAsync(scheduler, context.Background(), appRequest("beta", "apply"))
	expectNotStarted(t, exec)
	cancel()
	if resp := <-exclusive; resp.OK {
		t.Fatal("cancelled exclusive request reported success")
	}
	expectStarted(t, exec, "beta/apply")
	exec.release("beta/apply")
	<-queued
	exec.release("alpha/apply")
	<-running
}

func TestSchedulerQueueTimeDoesNotShortenExecutionBudget(t *testing.T) {
	scheduler, exec := newTestScheduler(t, 4)
	running := runAsync(scheduler, context.Background(), appRequest("alpha", "apply"))
	expectStarted(t, exec, "alpha/apply")
	queued := runAsync(scheduler, context.Background(), appRequest("alpha", "restart"))
	time.Sleep(200 * time.Millisecond)
	exec.release("alpha/apply")
	<-running
	expectStarted(t, exec, "alpha/restart")
	exec.mu.Lock()
	remaining := exec.deadline["alpha/restart"]
	exec.mu.Unlock()
	budget := RequestTimeout(appRequest("alpha", "restart"))
	if remaining < budget-100*time.Millisecond {
		t.Fatalf("execution budget = %v after queueing, want about %v", remaining, budget)
	}
	exec.release("alpha/restart")
	<-queued
}

func TestNewSchedulerRejectsInvalidLimit(t *testing.T) {
	if _, err := newScheduler(nil, nil, 0); err == nil {
		t.Fatal("zero concurrency limit accepted")
	}
}

func TestRequestLockKeys(t *testing.T) {
	cases := []struct {
		name string
		req  *Request
		want []string
	}{
		{"site", &Request{RequestType: "site", Site: &SiteRequest{Action: "create", Site: "alpha"}}, []string{"site:alpha"}},
		{"task", &Request{RequestType: "task", Task: &TaskRequest{Action: "apply", Site: "alpha"}}, []string{"site:alpha"}},
		{"vhost", &Request{RequestType: "vhost", Vhost: &VhostRequest{Action: "apply", Site: "alpha"}}, []string{"site:alpha"}},
		{"db with site", &Request{RequestType: "db", DB: &DBRequest{Action: "provision", Site: "alpha", Database: "alpha_db"}}, []string{"site:alpha", "database:alpha_db"}},
		{"db inventory", &Request{RequestType: "db", DB: &DBRequest{Action: "inventory"}}, []string{"dbctl:engine"}},
		{"git site", &Request{RequestType: "git", Git: &GitRequest{Action: "delete", Site: "alpha"}}, []string{"site:alpha"}},
		{"git clone", &Request{RequestType: "git", Git: &GitRequest{Action: "clone", Destination: "/var/www/sites/alpha/.stepanel-release-1"}}, []string{"git:/var/www/sites/alpha/.stepanel-release-1"}},
		{"certificate", &Request{RequestType: "certificate", Certificate: &CertificateRequest{Action: "issue", Domain: "example.org"}}, []string{"certificate:example.org"}},
		{"proxy is exclusive", &Request{RequestType: "proxy", Proxy: &ProxyRequest{Action: "reload"}}, nil},
		{"helper site", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "appctl", Action: "composer-install", Args: []string{"alpha", "/var/www/sites/alpha/public", "0", "1"}}}, []string{"site:alpha"}},
		{"helper account", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "appctl", Action: "account-resource-apply", Args: []string{"acme", "1", "2", "3", "4", "5", "6"}}}, []string{"account:acme"}},
		{"helper route delete", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "vhostctl", Action: "delete", Args: []string{"site-alpha-example.conf"}}}, []string{"vhostctl:host"}},
		{"helper proxy reload", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "proxyctl", Action: "reload"}}, []string{"proxyctl:host"}},
		{"dbctl provision", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "dbctl", Action: "provision", Args: []string{"alpha_db", "alpha_user", "alpha", "utf8mb4"}}}, []string{"database:alpha_db", "site:alpha"}},
		{"dbctl restore", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "dbctl", Action: "restore", Args: []string{"alpha_db", "alpha"}}}, []string{"database:alpha_db", "site:alpha"}},
		{"dbctl dump", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "dbctl", Action: "dump", Args: []string{"alpha_db"}}}, []string{"database:alpha_db"}},
		{"dbctl list", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "dbctl", Action: "list", Args: []string{"alpha"}}}, []string{"site:alpha"}},
		{"dbctl engine", &Request{RequestType: "helper", Helper: &HelperRequest{Name: "dbctl", Action: "terminate", Args: []string{"42"}}}, []string{"dbctl:engine"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestLockKeys(tc.req); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("keys = %v, want %v", got, tc.want)
			}
		})
	}
}

// Every allow-listed helper action except the explicitly host-scoped ones
// takes the site first; if a new action breaks that assumption it must be
// classified in helperLockKeys rather than silently keyed on the wrong value.
func TestHelperSchemasTakeSiteFirstUnlessClassified(t *testing.T) {
	classified := map[string]bool{
		"appctl/account-resource-apply": true,
		"proxyctl/delete":               true,
		"proxyctl/reload":               true,
		"vhostctl/delete":               true,
	}
	siteFirst := reflect.ValueOf(argSite).Pointer()
	for name, actions := range helperSchemas {
		if name == "dbctl" {
			continue
		}
		for action, spec := range actions {
			if classified[name+"/"+action] {
				continue
			}
			if len(spec.args) == 0 || reflect.ValueOf(spec.args[0]).Pointer() != siteFirst {
				t.Errorf("%s/%s does not take the site first; classify it in helperLockKeys", name, action)
			}
		}
	}
	for action := range helperSchemas["dbctl"] {
		spec := helperSchemas["dbctl"][action]
		switch action {
		case "list", "reconcile", "inventory", "diagnostics", "sessions", "settings", "terminate":
			continue
		}
		if reflect.ValueOf(spec.args[0]).Pointer() != reflect.ValueOf(argDatabase).Pointer() {
			t.Errorf("dbctl/%s does not take the database first; classify it in dbctlLockKeys", action)
		}
		position, hasSite := dbctlSitePosition[action]
		for i, arg := range spec.args {
			isSite := reflect.ValueOf(arg).Pointer() == siteFirst
			if isSite && (!hasSite || position != i) {
				t.Errorf("dbctl/%s takes a site at %d; record it in dbctlSitePosition", action, i)
			}
		}
	}
}

func TestHelperMutatesAccountsCoversSitectl(t *testing.T) {
	for action := range helperSchemas["sitectl"] {
		if !helperMutatesAccounts(&HelperRequest{Name: "sitectl", Action: action}) {
			t.Errorf("sitectl/%s must hold the account mutation lock", action)
		}
	}
	if helperMutatesAccounts(&HelperRequest{Name: "appctl", Action: "composer-install"}) {
		t.Error("appctl does not mutate host accounts")
	}
}
