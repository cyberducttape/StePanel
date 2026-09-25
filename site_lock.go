package main

import (
	"context"
	"errors"
	"log"

	"github.com/cyberducttape/StePanel/internal/operations"
)

// acquireSiteMutationLock composes the process-local lock with the SQLite
// fenced lease. The local lock keeps same-process callers cheap; the durable
// lease prevents a panel and external worker from mutating the same site at
// the same time. Tests that construct App without a database retain the local
// lock behavior.
func (a *App) acquireSiteMutationLock(ctx context.Context, key string) (func(), error) {
	localRelease := a.siteOperations.Acquire(key)
	if a.dbLocks == nil {
		return localRelease, nil
	}
	lease, err := a.dbLocks.Acquire(ctx, key)
	if err != nil {
		localRelease()
		return nil, err
	}
	holdCtx, cancelHold := context.WithCancel(context.Background())
	go func() {
		if err := a.dbLocks.Hold(holdCtx, lease); err != nil {
			log.Printf("durable site lock %s was lost: %v", key, err)
		}
	}()
	return func() {
		cancelHold()
		if err := a.dbLocks.Release(lease); err != nil && !errors.Is(err, operations.ErrLeaseLost) {
			log.Printf("release durable site lock %s: %v", key, err)
		}
		localRelease()
	}, nil
}
