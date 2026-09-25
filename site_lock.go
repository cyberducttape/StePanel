package main

import (
	"context"
	"errors"
	"log"
	"sort"

	"github.com/cyberducttape/StePanel/internal/operations"
)

// acquireSiteMutationLock composes the process-local lock with the SQLite
// fenced lease. The local lock keeps same-process callers cheap; the durable
// lease prevents a panel and external worker from mutating the same site at
// the same time. Tests that construct App without a database retain the local
// lock behavior.
func (a *App) acquireSiteMutationLock(ctx context.Context, key string) (func(), error) {
	_, release, err := a.acquireSiteMutationLockContext(ctx, key)
	return release, err
}

// acquireSiteMutationLockContext returns a child context that is cancelled
// when the durable lease is fenced by another owner. High-risk operations
// must pass this context to their helper calls; otherwise Hold would detect a
// lost lease but the operation could continue mutating host state.
func (a *App) acquireSiteMutationLockContext(ctx context.Context, key string) (context.Context, func(), error) {
	localRelease := a.siteOperations.Acquire(key)
	if a.dbLocks == nil {
		return ctx, localRelease, nil
	}
	lease, err := a.dbLocks.Acquire(ctx, key)
	if err != nil {
		localRelease()
		return nil, nil, err
	}
	operationCtx, cancelOperation := context.WithCancel(ctx)
	holdCtx, cancelHold := context.WithCancel(context.Background())
	go func() {
		if err := a.dbLocks.Hold(holdCtx, lease); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("durable site lock %s was lost: %v", key, err)
			cancelOperation()
		}
	}()
	return operationCtx, func() {
		cancelHold()
		cancelOperation()
		if err := a.dbLocks.Release(lease); err != nil && !errors.Is(err, operations.ErrLeaseLost) {
			log.Printf("release durable site lock %s: %v", key, err)
		}
		localRelease()
	}, nil
}

// acquireSiteMutationLocks is the compound-operation counterpart to
// acquireSiteMutationLock. Both local and durable locks use the same sorted,
// de-duplicated order so two processes cannot deadlock while acquiring a site
// and one of its dependent resources (for example, a vhost or proxy).
func (a *App) acquireSiteMutationLocks(ctx context.Context, keys ...string) (func(), error) {
	unique := make(map[string]struct{}, len(keys))
	ordered := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = struct{}{}
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	localRelease := a.siteOperations.AcquireMany(ordered...)
	if a.dbLocks == nil {
		return localRelease, nil
	}

	type heldLease struct {
		key    string
		lease  operations.Lease
		cancel context.CancelFunc
	}
	held := make([]heldLease, 0, len(ordered))
	for _, key := range ordered {
		lease, err := a.dbLocks.Acquire(ctx, key)
		if err != nil {
			for i := len(held) - 1; i >= 0; i-- {
				held[i].cancel()
				if releaseErr := a.dbLocks.Release(held[i].lease); releaseErr != nil && !errors.Is(releaseErr, operations.ErrLeaseLost) {
					log.Printf("release durable site lock %s after acquire failure: %v", held[i].key, releaseErr)
				}
			}
			localRelease()
			return nil, err
		}
		holdCtx, cancelHold := context.WithCancel(context.Background())
		held = append(held, heldLease{key: key, lease: lease, cancel: cancelHold})
		go func(key string, lease operations.Lease, holdCtx context.Context) {
			if err := a.dbLocks.Hold(holdCtx, lease); err != nil {
				log.Printf("durable site lock %s was lost: %v", key, err)
			}
		}(key, lease, holdCtx)
	}
	return func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].cancel()
			if err := a.dbLocks.Release(held[i].lease); err != nil && !errors.Is(err, operations.ErrLeaseLost) {
				log.Printf("release durable site lock %s: %v", held[i].key, err)
			}
		}
		localRelease()
	}, nil
}
