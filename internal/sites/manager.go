package sites

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ResourceEnvelope describes resource limits for a site
type ResourceEnvelope struct {
	CPULimitMillis   int64
	MemoryLimitBytes int64
	StorageLimitGB   int64
}

// Site represents a fully-managed site in StePanel
type Site struct {
	Name       string
	Status     string
	CreatedAt  time.Time
	WebRoot    string
	PHPVersion string
	Owner      string
}

// Provisioner implements the lifecycle-aware site provisioning
type Provisioner struct {
	webRoot string
}

// NewProvisioner creates a provisioner for managing site lifecycle
func NewProvisioner(webRoot string) *Provisioner {
	return &Provisioner{
		webRoot: webRoot,
	}
}

// CreateSite provisions a new site through the canonical lifecycle
func (p *Provisioner) CreateSite(ctx context.Context, siteName, webRoot, phpVersion, accountOwner string) error {
	if siteName == "" {
		return fmt.Errorf("site name is required")
	}
	if webRoot == "" {
		return fmt.Errorf("web root is required")
	}

	// Step 1: Create site filesystem structure
	publicRoot := filepath.Join(webRoot, "sites", siteName, "public")
	if err := os.MkdirAll(publicRoot, 0750); err != nil {
		return fmt.Errorf("failed to create site directory: %w", err)
	}

	// Step 2: Create recovery transaction record
	recoveryPath := filepath.Join(webRoot, "sites", siteName, ".recovery.json")
	recovery := map[string]interface{}{
		"site":       siteName,
		"created_at": time.Now().UTC(),
		"version":    1,
	}
	recoveryData, _ := json.Marshal(recovery)
	if err := os.WriteFile(recoveryPath, append(recoveryData, '\n'), 0600); err != nil {
		os.RemoveAll(filepath.Dir(publicRoot))
		return fmt.Errorf("failed to initialize recovery journal: %w", err)
	}

	// Step 3: Record PHP profile if specified
	if phpVersion != "" {
		phpPath := filepath.Join(webRoot, "sites", siteName, ".php")
		phpData := map[string]string{"version": phpVersion}
		phpJSON, _ := json.Marshal(phpData)
		if err := os.WriteFile(phpPath, append(phpJSON, '\n'), 0600); err != nil {
			os.RemoveAll(filepath.Dir(publicRoot))
			return fmt.Errorf("failed to record PHP profile: %w", err)
		}
	}

	return nil
}

// DeleteSite removes a provisioned site
func (p *Provisioner) DeleteSite(ctx context.Context, siteName string) error {
	if siteName == "" {
		return fmt.Errorf("site name is required")
	}
	siteDir := filepath.Join(p.webRoot, "sites", siteName)
	return os.RemoveAll(siteDir)
}
