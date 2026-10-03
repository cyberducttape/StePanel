package main

import "sync"

// Event-stream concurrency limits. Each open stream holds a socket, a
// goroutine, and a job subscription for up to its lifetime, so they are
// bounded per principal (a handful of dashboard tabs) and host-wide.
const (
	maxEventStreamsPerPrincipal = 8
	maxEventStreamsTotal        = 256
)

// streamLimiter counts open event streams. The zero value is ready to use.
type streamLimiter struct {
	mu           sync.Mutex
	total        int
	perPrincipal map[string]int
}

// acquire admits one stream for principal, returning a release function, or
// false when either limit is reached.
func (l *streamLimiter) acquire(principal string, perPrincipal, total int) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= total || l.perPrincipal[principal] >= perPrincipal {
		return nil, false
	}
	if l.perPrincipal == nil {
		l.perPrincipal = map[string]int{}
	}
	l.total++
	l.perPrincipal[principal]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.total--
			if l.perPrincipal[principal]--; l.perPrincipal[principal] <= 0 {
				delete(l.perPrincipal, principal)
			}
		})
	}, true
}

// status reports open streams and the configured limits.
func (l *streamLimiter) status() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return map[string]int{"active": l.total, "principals": len(l.perPrincipal), "limit_total": maxEventStreamsTotal, "limit_per_principal": maxEventStreamsPerPrincipal}
}
