package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"log"
)

// BrokerBridge provides a convenience wrapper around the root broker client.
// It bridges the main StePanel app with the typed root broker, allowing gradual
// migration from shell helpers to the Go-based broker.
type BrokerBridge struct {
	client *rootbroker.Client
	logger *log.Logger
}

// NewBrokerBridge creates a new broker bridge for the app.
func NewBrokerBridge(logger *log.Logger) (*BrokerBridge, error) {
	brokerPath := "/usr/local/sbin/stepanel-root"
	webRoot := "/var/www" // This should come from config in real usage

	client, err := rootbroker.NewClient(brokerPath, webRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create broker client: %w", err)
	}

	if logger == nil {
		logger = log.New(log.Writer(), "[broker-bridge] ", log.LstdFlags)
	}

	return &BrokerBridge{
		client: client,
		logger: logger,
	}, nil
}

// SiteCreate creates a new site via the broker.
// This replaces the shell helper call: runHelperCommand(ctx, config, config.SiteCtl, "create", site)
func (b *BrokerBridge) SiteCreate(ctx context.Context, site string, sshKeys string) error {
	b.logger.Printf("creating site via broker: %s", site)

	resp, err := b.client.SiteCreate(ctx, site, sshKeys)
	if err != nil {
		return fmt.Errorf("RPC failed: %w", err)
	}

	if !resp.OK {
		return fmt.Errorf("site creation failed: %s", resp.Error)
	}

	var result rootbroker.SiteResponse
	if len(resp.Details) > 0 {
		if err := json.Unmarshal(resp.Details, &result); err != nil {
			b.logger.Printf("warning: failed to unmarshal site response: %v", err)
		}
	}

	b.logger.Printf("site created: %s (user: %s)", site, result.Username)
	return nil
}

// SiteDelete deletes a site via the broker.
func (b *BrokerBridge) SiteDelete(ctx context.Context, site string) error {
	b.logger.Printf("deleting site via broker: %s", site)

	resp, err := b.client.SiteDelete(ctx, site)
	if err != nil {
		return fmt.Errorf("RPC failed: %w", err)
	}

	if !resp.OK {
		return fmt.Errorf("site deletion failed: %s", resp.Error)
	}

	b.logger.Printf("site deleted: %s", site)
	return nil
}

// AppApply applies app configuration via the broker.
func (b *BrokerBridge) AppApply(ctx context.Context, site string, version string, port int) error {
	b.logger.Printf("applying app config via broker: site=%s version=%s port=%d", site, version, port)

	resp, err := b.client.AppApply(ctx, site, version, port)
	if err != nil {
		return fmt.Errorf("RPC failed: %w", err)
	}

	if !resp.OK {
		return fmt.Errorf("app apply failed: %s", resp.Error)
	}

	b.logger.Printf("app config applied: %s", site)
	return nil
}

// DBProvision creates a new database via the broker.
func (b *BrokerBridge) DBProvision(ctx context.Context, site, database, username string) error {
	b.logger.Printf("provisioning database via broker: site=%s database=%s user=%s", site, database, username)

	resp, err := b.client.DBProvision(ctx, site, database, username)
	if err != nil {
		return fmt.Errorf("RPC failed: %w", err)
	}

	if !resp.OK {
		return fmt.Errorf("database provisioning failed: %s", resp.Error)
	}

	b.logger.Printf("database provisioned: %s", database)
	return nil
}

// VhostApply configures a virtual host via the broker.
func (b *BrokerBridge) VhostApply(ctx context.Context, site, domain, webserver string) error {
	b.logger.Printf("applying vhost via broker: domain=%s site=%s webserver=%s", domain, site, webserver)

	resp, err := b.client.VhostApply(ctx, site, domain, webserver)
	if err != nil {
		return fmt.Errorf("RPC failed: %w", err)
	}

	if !resp.OK {
		return fmt.Errorf("vhost apply failed: %s", resp.Error)
	}

	b.logger.Printf("vhost applied: %s", domain)
	return nil
}

// GitClone clones a git repository via the broker.
func (b *BrokerBridge) GitClone(ctx context.Context, repo, ref, destination string) error {
	b.logger.Printf("cloning git repo via broker: repo=%s ref=%s dest=%s", repo, ref, destination)

	resp, err := b.client.GitClone(ctx, repo, ref, destination)
	if err != nil {
		return fmt.Errorf("RPC failed: %w", err)
	}

	if !resp.OK {
		return fmt.Errorf("git clone failed: %s", resp.Error)
	}

	b.logger.Printf("git repo cloned: %s", repo)
	return nil
}

// ExecuteRaw allows executing a raw broker request.
// This is useful for operations not covered by the convenience methods.
func (b *BrokerBridge) ExecuteRaw(ctx context.Context, req *rootbroker.Request) (*rootbroker.Response, error) {
	return b.client.Execute(ctx, req)
}

// ExecuteRawWithErrorHandling executes a raw request and handles errors consistently.
func (b *BrokerBridge) ExecuteRawWithErrorHandling(ctx context.Context, req *rootbroker.Request, operation string) error {
	resp, err := b.client.Execute(ctx, req)
	if err != nil {
		return fmt.Errorf("RPC failed for %s: %w", operation, err)
	}

	if !resp.OK {
		return fmt.Errorf("%s failed: %s", operation, resp.Error)
	}

	return nil
}
