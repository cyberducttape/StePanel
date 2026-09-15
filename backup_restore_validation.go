package main

import (
	"fmt"
	"strings"
)

// RestoreDatabaseInput encapsulates database restore parameters
type RestoreDatabaseInput struct {
	Database       string
	TargetDatabase string
	TargetUser     string
	TargetPassword string
}

// ValidateRestoreDatabaseInput checks all database restore parameters
// Returns a slice of validation errors (empty if valid)
func ValidateRestoreDatabaseInput(cfg Config, input RestoreDatabaseInput, manifest BackupManifest) []string {
	var errs []string

	// Check if database restoration is even needed
	if input.Database == "" && input.TargetDatabase == "" && input.TargetUser == "" && input.TargetPassword == "" {
		// All fields empty = no database restore requested
		return errs
	}

	// If any field is provided, all must be provided
	if input.Database == "" {
		errs = append(errs, "database name is required when restoring a database")
	}
	if input.TargetDatabase == "" {
		errs = append(errs, "target_database name is required")
	}
	if input.TargetUser == "" {
		errs = append(errs, "target_user is required")
	}
	if input.TargetPassword == "" {
		errs = append(errs, "target_password is required")
	}

	// Database helper must be configured
	if cfg.DBCtl == "" {
		errs = append(errs, "database restore is not available on this system")
	}

	// Validate database name (source backup)
	dbLimit := databaseNameLimit(cfg)
	if input.Database != "" && len(input.Database) > 0 {
		if !validManagedDatabaseIdentifier(input.Database, dbLimit) {
			errs = append(errs, fmt.Sprintf("database name is invalid (max %d chars)", dbLimit))
		} else if !backupContainsDatabase(manifest, input.Database) {
			errs = append(errs, "database name does not exist in backup")
		}
	}

	// Validate target database name
	if input.TargetDatabase != "" && len(input.TargetDatabase) > 0 {
		if !validManagedDatabaseIdentifier(input.TargetDatabase, dbLimit) {
			errs = append(errs, fmt.Sprintf("target_database name is invalid (max %d chars)", dbLimit))
		}
	}

	// Validate target user (strict rules: must start with lowercase letter)
	if input.TargetUser != "" {
		if len(input.TargetUser) == 0 {
			errs = append(errs, "target_user cannot be empty")
		} else if len(input.TargetUser) > 32 {
			errs = append(errs, "target_user exceeds 32 character limit")
		} else if input.TargetUser[0] < 'a' || input.TargetUser[0] > 'z' {
			errs = append(errs, "target_user must start with a lowercase letter")
		} else if !validManagedDatabaseIdentifier(input.TargetUser, 32) {
			errs = append(errs, "target_user contains invalid characters (alphanumeric and underscore only)")
		}
	}

	// Validate target password
	if input.TargetPassword != "" && len(input.TargetPassword) > 0 {
		if !validDatabasePassword(input.TargetPassword) {
			errs = append(errs, "target_password does not meet minimum requirements")
		}
	}

	return errs
}

// ValidateRestoreStagingInput checks restore-to-staging parameters
func ValidateRestoreStagingInput(input RestoreToStagingRequest) []string {
	var errs []string

	input.Site = strings.TrimSpace(input.Site)
	input.Backup = strings.TrimSpace(input.Backup)
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))

	if input.Site == "" {
		errs = append(errs, "site name is required")
	} else if safeUser(input.Site) == "" {
		errs = append(errs, "site name is invalid")
	}

	if input.Backup == "" {
		errs = append(errs, "backup name is required")
	} else if !validBackupName(input.Backup) {
		errs = append(errs, "backup name is invalid")
	}

	if input.Domain == "" {
		errs = append(errs, "domain name is required")
	} else if !domainPattern.MatchString(input.Domain) {
		errs = append(errs, "domain name is invalid")
	}

	return errs
}
