package main

import (
	"context"
	"errors"
	"testing"
)

func TestCloneManagedDatabaseToStagingContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cloneManagedDatabaseToStagingContext(ctx, Config{}, "source", "target", "user", "password", AuthorizedSite{site: "site"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cloneManagedDatabaseToStagingContext error = %v, want context.Canceled", err)
	}
}

func TestStagingBasicAuthSupportMatchesVHostHelpers(t *testing.T) {
	for _, webserver := range []string{"caddy", "apache"} {
		if !stagingBasicAuthSupported(webserver) {
			t.Fatalf("%s should support staging Basic Auth", webserver)
		}
	}
	for _, webserver := range []string{"openlitespeed", "", "nginx"} {
		if stagingBasicAuthSupported(webserver) {
			t.Fatalf("%s must not advertise unsupported staging Basic Auth", webserver)
		}
	}
}
