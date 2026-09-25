package main

import (
	"context"
	"errors"
	"testing"
)

func TestActivateStagedSiteHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&App{Config: Config{WebRoot: t.TempDir()}}).activateStagedSite(ctx, "demo", t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("activateStagedSite error = %v, want context.Canceled", err)
	}
}
