package doctor

import (
	"fmt"
	"strings"
	"time"
)

// Analyzer performs migration compatibility analysis
type Analyzer struct {
	compatibilityMatrix CompatibilityMatrix
}

// NewAnalyzer creates a new migration analyzer
func NewAnalyzer() *Analyzer {
	return &Analyzer{
		compatibilityMatrix: getDefaultCompatibilityMatrix(),
	}
}

// Analyze compares source and destination inventories
func (a *Analyzer) Analyze(source, destination ServerInventory) *MigrationAnalysis {
	analysis := &MigrationAnalysis{
		SourceInventory:      source,
		DestinationInventory: destination,
		AnalysisTime:         time.Now().UTC(),
	}

	// Check all compatibility issues
	a.checkPHPCompatibility(analysis)
	a.checkDatabaseCompatibility(analysis)
	a.checkDiskSpace(analysis)
	a.checkExtensions(analysis)
	a.checkSQLModes(analysis)
	a.checkExternalServices(analysis)
	a.checkOperatingSystem(analysis)

	// Determine readiness
	analysis.ReadyForMigration = len(analysis.Blockers) == 0
	analysis.EstimatedDataGB = a.estimateMigrationSize(source)

	// Generate recommended actions
	analysis.RecommendedActions = a.generateRecommendations(analysis)

	return analysis
}

func (a *Analyzer) checkPHPCompatibility(analysis *MigrationAnalysis) {
	src := analysis.SourceInventory.PHP.Version
	dst := analysis.DestinationInventory.PHP.Version

	if src == "" {
		analysis.Blockers = append(analysis.Blockers, Issue{
			Severity:    "blocker",
			Category:    "php_version",
			Title:       "PHP version not detected on source",
			Description: "Could not determine PHP version on source server",
			Impact:      "Cannot assess compatibility; migration may fail",
			Solution:    "Verify PHP is installed on source server and accessible",
		})
		return
	}

	if dst == "" {
		analysis.Warnings = append(analysis.Warnings, Issue{
			Severity:    "warning",
			Category:    "php_version",
			Title:       "PHP version not detected on destination",
			Description: fmt.Sprintf("Source runs PHP %s but destination PHP version is unknown", src),
			Impact:      "Cannot verify PHP compatibility",
			Solution:    "Ensure PHP is installed on destination before migration",
		})
		return
	}

	// Check for major version mismatch
	srcMajor := strings.Split(src, ".")[0]
	dstMajor := strings.Split(dst, ".")[0]

	if srcMajor != dstMajor {
		analysis.Warnings = append(analysis.Warnings, Issue{
			Severity:    "warning",
			Category:    "php_version",
			Title:       fmt.Sprintf("PHP major version changed (%.1s → %.1s)", src, dst),
			Description: fmt.Sprintf("Source runs PHP %s; destination has PHP %s. Major version upgrades may require code changes.", src, dst),
			Impact:      "Application may fail due to deprecated functions or changed behavior",
			Solution:    "Test application thoroughly after migration. Review PHP migration guides for breaking changes.",
		})
	}
}

func (a *Analyzer) checkDatabaseCompatibility(analysis *MigrationAnalysis) {
	src := analysis.SourceInventory.Database
	dst := analysis.DestinationInventory.Database

	// Check database type compatibility
	if src.Type == "" {
		analysis.Warnings = append(analysis.Warnings, Issue{
			Severity:    "warning",
			Category:    "database_type",
			Title:       "Source database type not detected",
			Description: "Could not determine database type on source server",
			Impact:      "Cannot verify database compatibility",
			Solution:    "Ensure database is running and accessible on source",
		})
		return
	}

	if dst.Type == "" {
		analysis.Warnings = append(analysis.Warnings, Issue{
			Severity:    "warning",
			Category:    "database_type",
			Title:       "Destination database type not detected",
			Description: fmt.Sprintf("Source has %s; destination type is unknown", src.Type),
			Impact:      "Cannot verify database compatibility",
			Solution:    "Ensure database is installed and running on destination",
		})
		return
	}

	// MySQL/MariaDB are compatible with each other
	isMysqlFamily := func(dbType string) bool {
		return strings.Contains(strings.ToLower(dbType), "mysql") || strings.Contains(strings.ToLower(dbType), "mariadb")
	}

	srcMysql := isMysqlFamily(src.Type)
	dstMysql := isMysqlFamily(dst.Type)

	if srcMysql != dstMysql {
		analysis.Blockers = append(analysis.Blockers, Issue{
			Severity:    "blocker",
			Category:    "database_type",
			Title:       fmt.Sprintf("Database type mismatch: %s → %s", src.Type, dst.Type),
			Description: fmt.Sprintf("Source uses %s; destination uses %s. These are incompatible database systems.", src.Type, dst.Type),
			Impact:      "BLOCKER: Data migration will fail completely",
			Solution:    "Install compatible database on destination, or use application backup/restore instead of direct database migration",
		})
		return
	}

	// Check version compatibility
	if src.Version != "" && dst.Version != "" {
		srcMajor := strings.Split(src.Version, ".")[0]
		dstMajor := strings.Split(dst.Version, ".")[0]

		if srcMajor != dstMajor {
			analysis.Warnings = append(analysis.Warnings, Issue{
				Severity:    "warning",
				Category:    "database_version",
				Title:       fmt.Sprintf("Database major version changed: %s → %s", src.Version, dst.Version),
				Description: fmt.Sprintf("Source has %s %s; destination has %s %s", src.Type, src.Version, dst.Type, dst.Version),
				Impact:      "Dump restore may fail due to compatibility issues",
				Solution:    "Test database restore carefully. Review compatibility notes between versions.",
			})
		}
	}
}

func (a *Analyzer) checkExtensions(analysis *MigrationAnalysis) {
	srcExt := make(map[string]bool)
	for _, ext := range analysis.SourceInventory.PHP.Extensions {
		srcExt[ext] = true
	}

	dstExt := make(map[string]bool)
	for _, ext := range analysis.DestinationInventory.PHP.Extensions {
		dstExt[ext] = true
	}

	// Check for missing critical extensions
	criticalExtensions := []string{"mysqli", "pdo", "curl", "json", "mbstring", "openssl"}

	for _, ext := range criticalExtensions {
		if srcExt[ext] && !dstExt[ext] {
			analysis.Blockers = append(analysis.Blockers, Issue{
				Severity:    "blocker",
				Category:    "php_extension",
				Title:       fmt.Sprintf("Missing critical PHP extension: %s", ext),
				Description: fmt.Sprintf("Source uses %s but destination does not have it installed", ext),
				Impact:      "BLOCKER: Application will fail at runtime with missing function errors",
				Solution:    fmt.Sprintf("Install PHP extension '%s' on destination before migration", ext),
			})
		}
	}

	// Check for missing optional extensions
	for ext := range srcExt {
		if !dstExt[ext] && !isStandardExtension(ext) {
			// Only warn about extensions used in detected applications
			for _, site := range analysis.SourceInventory.Sites {
				if site.Application != "" {
					analysis.Warnings = append(analysis.Warnings, Issue{
						Severity:    "warning",
						Category:    "php_extension",
						Title:       fmt.Sprintf("Missing optional PHP extension: %s", ext),
						Description: fmt.Sprintf("Source has extension '%s' which is not available on destination", ext),
						Impact:      "Application features using this extension will not work",
						Solution:    fmt.Sprintf("Install '%s' before migration, or verify application doesn't depend on it", ext),
						Evidence:    []string{site.Domain},
					})
					break
				}
			}
		}
	}
}

func (a *Analyzer) checkDiskSpace(analysis *MigrationAnalysis) {
	estimated := a.estimateMigrationSize(analysis.SourceInventory)
	available := analysis.DestinationInventory.SystemResources.AvailableDiskGB

	if estimated > available {
		analysis.Blockers = append(analysis.Blockers, Issue{
			Severity:    "blocker",
			Category:    "disk_space",
			Title:       fmt.Sprintf("Insufficient disk space: %d GB needed, %d GB available", estimated, available),
			Description: fmt.Sprintf("Migration requires ~%d GB but destination only has %d GB available", estimated, available),
			Impact:      "BLOCKER: Migration will fail when extracting files or restoring databases",
			Solution:    fmt.Sprintf("Free up at least %d GB on destination, or upgrade storage", estimated),
		})
		return
	}

	if estimated > available/2 {
		analysis.Warnings = append(analysis.Warnings, Issue{
			Severity:    "warning",
			Category:    "disk_space",
			Title:       fmt.Sprintf("Low disk space margin: %d GB needed, %d GB available", estimated, available),
			Description: fmt.Sprintf("Migration uses %d GB of %d GB available. Limited headroom for temporary files.", estimated, available),
			Impact:      "Migration may fail if temporary files exceed available space",
			Solution:    fmt.Sprintf("Free up additional space or add storage before migration"),
		})
	}
}

func (a *Analyzer) checkSQLModes(analysis *MigrationAnalysis) {
	src := analysis.SourceInventory.Database.SQLMode
	dst := analysis.DestinationInventory.Database.SQLMode

	if src == "" || dst == "" {
		return
	}

	if src != dst {
		analysis.Warnings = append(analysis.Warnings, Issue{
			Severity:    "warning",
			Category:    "sql_mode",
			Title:       "MySQL SQL mode differs between source and destination",
			Description: fmt.Sprintf("Source: %s\nDestination: %s", src, dst),
			Impact:      "Queries or constraints that work on source may fail on destination",
			Solution:    "Review SQL differences. Set destination SQL mode to match source if needed.",
		})
	}
}

func (a *Analyzer) checkExternalServices(analysis *MigrationAnalysis) {
	for _, service := range analysis.SourceInventory.ExternalServices {
		if !service.Reachable {
			continue // Skip unreachable services on source
		}

		// Check if destination can reach the service
		dstReachable := false
		for _, dstService := range analysis.DestinationInventory.ExternalServices {
			if dstService.Type == service.Type && dstService.Host == service.Host {
				dstReachable = dstService.Reachable
				break
			}
		}

		if !dstReachable {
			analysis.Warnings = append(analysis.Warnings, Issue{
				Severity:    "warning",
				Category:    "external_service",
				Title:       fmt.Sprintf("External service unreachable from destination: %s", service.Description),
				Description: fmt.Sprintf("Source can reach %s at %s:%d, but destination cannot", service.Description, service.Host, service.Port),
				Impact:      "Application features relying on this service will fail after migration",
				Solution:    fmt.Sprintf("Verify network connectivity, firewall rules, and credentials for %s", service.Description),
			})
		}
	}
}

func (a *Analyzer) checkOperatingSystem(analysis *MigrationAnalysis) {
	src := analysis.SourceInventory.OS
	dst := analysis.DestinationInventory.OS

	if src.Name == "" || dst.Name == "" {
		return
	}

	// Warn about major OS changes
	if src.DistributorID != dst.DistributorID {
		analysis.InfoMessages = append(analysis.InfoMessages,
			fmt.Sprintf("Operating system changed: %s → %s. This may require package manager adjustments.", src.Name, dst.Name))
	}
}

func (a *Analyzer) estimateMigrationSize(inv ServerInventory) int64 {
	total := int64(0)
	for _, site := range inv.Sites {
		total += site.DiskUsageMB
	}
	// Add 20% buffer for database dumps and temporary files
	return (total / 1024) + ((total / 1024) / 5) + 1
}

func (a *Analyzer) generateRecommendations(analysis *MigrationAnalysis) []string {
	recommendations := []string{}

	if len(analysis.Blockers) > 0 {
		recommendations = append(recommendations, "CRITICAL: Fix all blockers before proceeding with migration")
	}

	if len(analysis.Warnings) > 0 {
		recommendations = append(recommendations, "Review warnings carefully - they may cause issues during or after migration")
	}

	if analysis.EstimatedDataGB > 50 {
		recommendations = append(recommendations, "Large migration (>50GB) - consider scheduling during maintenance window")
	}

	if analysis.SourceInventory.PHP.Version != analysis.DestinationInventory.PHP.Version {
		recommendations = append(recommendations, "Test application thoroughly after migration due to PHP version change")
	}

	recommendations = append(recommendations, "Create a backup of destination before starting migration")
	recommendations = append(recommendations, "Consider test restore on staging environment first")

	return recommendations
}

func isStandardExtension(ext string) bool {
	standard := map[string]bool{
		"core": true, "standard": true, "pcre": true, "date": true, "intl": true,
		"reflection": true, "spl": true, "hash": true, "json": true,
	}
	return standard[ext]
}

func getDefaultCompatibilityMatrix() CompatibilityMatrix {
	return CompatibilityMatrix{
		PHPVersions: []string{"5.6", "7.0", "7.1", "7.2", "7.3", "7.4", "8.0", "8.1", "8.2", "8.3"},
		DatabaseVersions: map[string][]string{
			"MySQL":      {"5.6", "5.7", "8.0"},
			"MariaDB":    {"10.1", "10.2", "10.3", "10.4", "10.5", "10.6", "11.0"},
			"PostgreSQL": {"9.6", "10", "11", "12", "13", "14", "15"},
		},
		Extensions: map[string][]string{
			"mysqli":   {"5.6", "7.0", "7.1", "7.2", "7.3", "7.4", "8.0", "8.1", "8.2"},
			"pdo":      {"5.6", "7.0", "7.1", "7.2", "7.3", "7.4", "8.0", "8.1", "8.2"},
			"curl":     {"5.6", "7.0", "7.1", "7.2", "7.3", "7.4", "8.0", "8.1", "8.2"},
			"redis":    {"5.6", "7.0", "7.1", "7.2", "7.3", "7.4", "8.0", "8.1", "8.2"},
			"igbinary": {"5.6", "7.0", "7.1", "7.2", "7.3", "7.4", "8.0", "8.1"},
			"mbstring": {"5.6", "7.0", "7.1", "7.2", "7.3", "7.4", "8.0", "8.1", "8.2"},
		},
	}
}
