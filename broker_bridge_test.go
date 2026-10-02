package main

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

func brokerTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type successfulBrokerClient struct{}

func (successfulBrokerClient) response() (*rootbroker.Response, error) {
	return &rootbroker.Response{OK: true}, nil
}
func (successfulBrokerClient) SiteCreate(context.Context, string, string) (*rootbroker.Response, error) {
	return successfulBrokerClient{}.response()
}
func (successfulBrokerClient) SiteDelete(context.Context, string) (*rootbroker.Response, error) {
	return successfulBrokerClient{}.response()
}
func (successfulBrokerClient) AppApply(context.Context, string, string, int) (*rootbroker.Response, error) {
	return successfulBrokerClient{}.response()
}
func (successfulBrokerClient) DBProvision(context.Context, string, string, string, string, string) (*rootbroker.Response, error) {
	return successfulBrokerClient{}.response()
}
func (successfulBrokerClient) VhostApply(context.Context, string, string, string) (*rootbroker.Response, error) {
	return successfulBrokerClient{}.response()
}
func (successfulBrokerClient) GitClone(context.Context, string, string, string) (*rootbroker.Response, error) {
	return successfulBrokerClient{}.response()
}
func (successfulBrokerClient) Execute(context.Context, *rootbroker.Request) (*rootbroker.Response, error) {
	return successfulBrokerClient{}.response()
}

func successfulBrokerBridge(t *testing.T) *BrokerBridge {
	t.Helper()
	return &BrokerBridge{client: successfulBrokerClient{}, logger: log.New(os.Stderr, "[test] ", 0)}
}

func TestBrokerBridge_NewBrokerBridge(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	bridge, err := NewBrokerBridge(logger)
	if err != nil {
		t.Fatalf("NewBrokerBridge failed: %v", err)
	}

	if bridge == nil {
		t.Error("Bridge is nil")
	}
	if bridge.client == nil {
		t.Error("Client is nil")
	}
}

func TestBrokerBridge_SiteCreate(t *testing.T) {
	bridge := successfulBrokerBridge(t)

	ctx := brokerTestContext(t)
	if err := bridge.SiteCreate(ctx, "testsite", ""); err != nil {
		t.Fatalf("SiteCreate failed: %v", err)
	}
}

func TestBrokerBridge_SiteDelete(t *testing.T) {
	bridge := successfulBrokerBridge(t)

	ctx := brokerTestContext(t)
	// This will likely succeed (even without root, delete might be no-op)
	if err := bridge.SiteDelete(ctx, "testsite"); err != nil {
		t.Fatalf("SiteDelete failed: %v", err)
	}
}

func TestBrokerBridge_AppApply(t *testing.T) {
	bridge := successfulBrokerBridge(t)

	ctx := brokerTestContext(t)
	if err := bridge.AppApply(ctx, "testsite", "18.0.0", 3000); err != nil {
		t.Fatalf("AppApply failed: %v", err)
	}
}

func TestBrokerBridge_DBProvision(t *testing.T) {
	bridge := successfulBrokerBridge(t)

	ctx := brokerTestContext(t)
	if err := bridge.DBProvision(ctx, "testsite", "testdb", "testuser", "utf8mb4", "strong-test-password-2026"); err != nil {
		t.Fatalf("DBProvision failed: %v", err)
	}
}

func TestBrokerBridge_VhostApply(t *testing.T) {
	bridge := successfulBrokerBridge(t)

	ctx := brokerTestContext(t)
	if err := bridge.VhostApply(ctx, "testsite", "example.com", "caddy"); err != nil {
		t.Fatalf("VhostApply failed: %v", err)
	}
}

func TestBrokerBridge_GitClone(t *testing.T) {
	bridge := successfulBrokerBridge(t)

	ctx := brokerTestContext(t)
	if err := bridge.GitClone(ctx, "https://github.com/user/repo.git", "main", "dest"); err != nil {
		t.Fatalf("GitClone failed: %v", err)
	}
}
