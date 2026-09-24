package importer

import (
	"context"
	"fmt"
)

// SiteProvisioner interface for provisioning managed sites
type SiteProvisioner interface {
	// CreateSite provisions a new site with full lifecycle
	CreateSite(ctx context.Context, siteName, webRoot, phpVersion, accountOwner string) error
	// DeleteSite removes a provisioned site
	DeleteSite(ctx context.Context, siteName string) error
}

// LifecycleAwareImporter wraps archive import with the canonical site lifecycle
type LifecycleAwareImporter struct {
	executor    *Executor
	provisioner SiteProvisioner
}

// NewLifecycleAwareImporter creates an importer that uses the full site lifecycle
func NewLifecycleAwareImporter(executor *Executor, provisioner SiteProvisioner) *LifecycleAwareImporter {
	return &LifecycleAwareImporter{
		executor:    executor,
		provisioner: provisioner,
	}
}

// Import performs archive import through the canonical site lifecycle
func (lai *LifecycleAwareImporter) Import(
	ctx context.Context,
	req *ArchiveImportRequest,
	webRoot string,
	onProgress func(*ImportJob),
) (*ImportResult, error) {
	// Phase 1: Create site through the canonical lifecycle
	if err := lai.provisioner.CreateSite(ctx, req.SiteName, webRoot, "", ""); err != nil {
		return nil, fmt.Errorf("site provisioning failed: %w", err)
	}

	// Phase 2: Extract archive to provisioned site
	result, err := lai.executor.ExecuteImport(ctx, req, webRoot, onProgress)
	if err != nil {
		// Rollback site creation on extraction failure
		if rollbackErr := lai.provisioner.DeleteSite(ctx, req.SiteName); rollbackErr != nil {
			fmt.Printf("warning: site provisioning rolled back but cleanup failed: %v\n", rollbackErr)
		}
		return nil, fmt.Errorf("archive extraction failed: %w", err)
	}

	// Mark site as ready now that import succeeded
	result.SiteStatus = "ready"
	result.NextSteps = []string{
		"Verify site configuration and SSL certificates",
		"Restore database if needed",
		"Update DNS to point to this panel",
	}

	return result, nil
}
