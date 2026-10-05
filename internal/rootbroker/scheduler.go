package rootbroker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// DefaultMaxConcurrent bounds how many privileged operations the broker runs
// at once. Long operations (package builds, certificate issuance) hold a slot
// for minutes, so the bound must leave room for short lifecycle mutations.
const DefaultMaxConcurrent = 8

// Scheduler admits validated requests to the broker. Operations on unrelated
// resources run concurrently; operations on the same resource (a site, a
// database, a certificate domain) serialize. A request whose resources cannot
// be derived runs exclusively, which preserves the broker's historical
// one-at-a-time semantics for anything not explicitly scoped. Health probes
// bypass admission entirely so a busy broker is never reported unhealthy.
//
// Host-wide shared state (web server configuration, systemd units, the
// account database, database engine catalogs) is serialized by the helpers'
// own flock(1) locks and by Broker.accountMutationMu, not here.
type Scheduler struct {
	execute  func(context.Context, *Request) (*Response, error)
	validate func(*Request) error
	gate     *admissionGate
	keys     *keyedLocks
}

// NewScheduler wraps broker with resource-scoped admission and at most
// maxConcurrent simultaneous operations.
func NewScheduler(broker *Broker, maxConcurrent int) (*Scheduler, error) {
	if broker == nil {
		return nil, errors.New("broker is required")
	}
	return newScheduler(broker.Execute, broker.validator.ValidateRequest, maxConcurrent)
}

func newScheduler(execute func(context.Context, *Request) (*Response, error), validate func(*Request) error, maxConcurrent int) (*Scheduler, error) {
	if maxConcurrent < 1 {
		return nil, fmt.Errorf("max concurrent operations must be at least 1, got %d", maxConcurrent)
	}
	return &Scheduler{
		execute:  execute,
		validate: validate,
		gate:     newAdmissionGate(maxConcurrent),
		keys:     newKeyedLocks(),
	}, nil
}

// Execute waits for admission and runs req. Waiting is bounded by the
// request's own broker timeout and by ctx; once admitted, the operation gets
// its full RequestTimeout budget, so time spent queued never shortens the
// window a helper has to finish or roll back.
func (s *Scheduler) Execute(ctx context.Context, req *Request) (*Response, error) {
	timeout := RequestTimeout(req)
	if req == nil || req.RequestType == "health" || s.validate(req) != nil {
		// Health is a no-op and invalid requests are rejected by validation
		// before any host access; neither needs admission.
		execCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return s.execute(execCtx, req)
	}

	keys := requestLockKeys(req)
	waitCtx, cancelWait := context.WithTimeout(ctx, timeout)
	defer cancelWait()
	release, err := s.keys.acquire(waitCtx, keys)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("root broker is busy: %v", err)}, nil
	}
	defer release()
	exclusive := len(keys) == 0
	if err := s.gate.acquire(waitCtx, exclusive); err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("root broker is busy: %v", err)}, nil
	}
	defer s.gate.release(exclusive)

	execCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return s.execute(execCtx, req)
}

// requestLockKeys derives the resources a validated request mutates. An empty
// result means the request is not scoped and must run exclusively.
func requestLockKeys(req *Request) []string {
	var keys []string
	add := func(kind, name string) {
		if name != "" {
			keys = append(keys, kind+":"+name)
		}
	}
	switch req.RequestType {
	case "site":
		add("site", req.Site.Site)
	case "app":
		add("site", req.App.Site)
	case "worker":
		add("site", req.Worker.Site)
	case "runner":
		add("site", req.Runner.Site)
	case "task":
		add("site", req.Task.Site)
	case "environment":
		add("site", req.Environment.Site)
	case "resource":
		if req.Resource.Action == "apply-account" {
			add("account", req.Resource.Account)
		} else {
			add("site", req.Resource.Site)
		}
	case "vhost":
		if req.Vhost.Action == "delete" {
			// Route names, not sites; vhostctl flocks the shared web server
			// configuration it rewrites.
			add("vhostctl", "host")
		} else {
			add("site", req.Vhost.Site)
		}
	case "proxy":
		// A bare reload touches every route and stays exclusive.
		switch req.Proxy.Action {
		case "apply":
			add("site", req.Proxy.Site)
		case "delete":
			add("proxyctl", "host")
		}
	case "db":
		add("site", req.DB.Site)
		add("database", req.DB.Database)
		if len(keys) == 0 || req.DB.Action == "reconcile" || req.DB.Action == "inventory" {
			// Engine-wide reads (inventory); dbctl locks its own catalog.
			add("dbctl", "engine")
		}
	case "git":
		if req.Git.Site != "" {
			add("site", req.Git.Site)
		} else {
			add("git", req.Git.Destination)
		}
	case "certificate":
		add("certificate", req.Certificate.Domain)
	}
	return keys
}

// admissionGate bounds concurrent operations and lets an exclusive operation
// wait for all running ones to drain. Once an exclusive request is waiting,
// new shared requests queue behind it so it cannot be starved.
type admissionGate struct {
	mu               sync.Mutex
	limit            int
	active           int
	exclusive        bool
	waitingExclusive int
	changed          chan struct{}
}

func newAdmissionGate(limit int) *admissionGate {
	return &admissionGate{limit: limit, changed: make(chan struct{})}
}

// broadcastLocked wakes every waiter so it re-evaluates admission.
func (g *admissionGate) broadcastLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *admissionGate) acquire(ctx context.Context, exclusive bool) error {
	g.mu.Lock()
	if exclusive {
		g.waitingExclusive++
	}
	for {
		if exclusive && !g.exclusive && g.active == 0 {
			g.waitingExclusive--
			g.exclusive = true
			g.mu.Unlock()
			return nil
		}
		if !exclusive && !g.exclusive && g.waitingExclusive == 0 && g.active < g.limit {
			g.active++
			g.mu.Unlock()
			return nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-changed:
			g.mu.Lock()
		case <-ctx.Done():
			if exclusive {
				g.mu.Lock()
				g.waitingExclusive--
				g.broadcastLocked()
				g.mu.Unlock()
			}
			return fmt.Errorf("waiting for an execution slot: %w", ctx.Err())
		}
	}
}

func (g *admissionGate) release(exclusive bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if exclusive {
		g.exclusive = false
	} else {
		g.active--
	}
	g.broadcastLocked()
}

// keyedLocks provides context-aware exclusive locks per resource key. Keys are
// acquired in sorted order so multi-resource requests cannot deadlock.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	held chan struct{}
	refs int
}

func newKeyedLocks() *keyedLocks {
	return &keyedLocks{locks: make(map[string]*keyedLock)}
}

func (k *keyedLocks) acquire(ctx context.Context, keys []string) (func(), error) {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	unique := sorted[:0]
	for i, key := range sorted {
		if i == 0 || key != sorted[i-1] {
			unique = append(unique, key)
		}
	}
	held := make([]string, 0, len(unique))
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			k.unlock(held[i])
		}
	}
	for _, key := range unique {
		lock := k.ref(key)
		select {
		case lock.held <- struct{}{}:
			held = append(held, key)
		case <-ctx.Done():
			k.unref(key)
			release()
			return nil, fmt.Errorf("waiting for %s: %w", key, ctx.Err())
		}
	}
	return release, nil
}

func (k *keyedLocks) ref(key string) *keyedLock {
	k.mu.Lock()
	defer k.mu.Unlock()
	lock, ok := k.locks[key]
	if !ok {
		lock = &keyedLock{held: make(chan struct{}, 1)}
		k.locks[key] = lock
	}
	lock.refs++
	return lock
}

func (k *keyedLocks) unref(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	lock := k.locks[key]
	lock.refs--
	if lock.refs == 0 {
		delete(k.locks, key)
	}
}

func (k *keyedLocks) unlock(key string) {
	k.mu.Lock()
	lock := k.locks[key]
	k.mu.Unlock()
	<-lock.held
	k.unref(key)
}
