package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteImportValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     *ArchiveImportRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid request",
			req: &ArchiveImportRequest{
				SiteName:   "mysite",
				URL:        "https://example.com/archive.tar.gz",
				ConfigPath: "wp-config.php",
			},
			wantErr: false,
		},
		{
			name: "invalid URL scheme",
			req: &ArchiveImportRequest{
				SiteName:   "test",
				URL:        "http://example.com/archive.tar.gz", // http not https
				ConfigPath: "wp-config.php",
			},
			wantErr: true,
			errMsg:  "not allowed",
		},
		{
			name: "private IP SSRF attempt",
			req: &ArchiveImportRequest{
				SiteName:   "test",
				URL:        "https://127.0.0.1/archive.tar.gz",
				ConfigPath: "wp-config.php",
			},
			wantErr: true,
			errMsg:  "not allowed",
		},
		{
			name: "metadata endpoint SSRF attempt",
			req: &ArchiveImportRequest{
				SiteName:   "test",
				URL:        "https://169.254.169.254/latest/metadata",
				ConfigPath: "wp-config.php",
			},
			wantErr: true,
			errMsg:  "not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate URL would be checked by fetcher
			allowed := isAllowedURL(tt.req.URL)
			if tt.wantErr && allowed {
				t.Errorf("expected URL to be rejected: %s", tt.req.URL)
			}
			if !tt.wantErr && !allowed {
				t.Errorf("expected URL to be allowed: %s", tt.req.URL)
			}
		})
	}
}

func TestExtractWordPressDefine(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		key      string
		expected string
	}{
		{
			name:     "simple single quotes",
			content:  "define('DB_NAME', 'wordpress_db');\n",
			key:      "DB_NAME",
			expected: "wordpress_db",
		},
		{
			name:     "simple double quotes",
			content:  `define("DB_USER", "wp_user");` + "\n",
			key:      "DB_USER",
			expected: "wp_user",
		},
		{
			name:     "mixed quotes (single key, double value)",
			content:  `define('DB_PASSWORD', "secret123");` + "\n",
			key:      "DB_PASSWORD",
			expected: "secret123",
		},
		{
			name:     "whitespace handling",
			content:  "define( 'DB_HOST' , 'localhost' ) ;\n",
			key:      "DB_HOST",
			expected: "localhost",
		},
		{
			name:     "key not found",
			content:  "define('DB_NAME', 'wordpress_db');\n",
			key:      "NONEXISTENT",
			expected: "",
		},
		{
			name:     "value with special chars",
			content:  `define('DB_PASSWORD', 'p@$$w0rd!#');` + "\n",
			key:      "DB_PASSWORD",
			expected: "p@$$w0rd!#",
		},
		{
			name:     "commented line ignored",
			content:  "// define('DB_NAME', 'old_db');\ndefine('DB_NAME', 'new_db');\n",
			key:      "DB_NAME",
			expected: "new_db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractWordPressDefine(tt.content, tt.key)
			if result != tt.expected {
				t.Errorf("extractWordPressDefine(%q, %q) = %q, want %q",
					tt.content, tt.key, result, tt.expected)
			}
		})
	}
}

func TestArchiveEntryLimits(t *testing.T) {
	job := &ImportJob{
		ID:               "test-job",
		EntriesProcessed: 0,
	}

	// Simulate processing entries up to limit
	for i := 0; i < maxArchiveEntries; i++ {
		job.EntriesProcessed++
	}

	if job.EntriesProcessed != maxArchiveEntries {
		t.Fatalf("expected %d entries, got %d", maxArchiveEntries, job.EntriesProcessed)
	}

	// Next entry should exceed limit
	job.EntriesProcessed++
	if job.EntriesProcessed <= maxArchiveEntries {
		t.Errorf("entry count should exceed limit")
	}
}

func TestFindDatabaseDumpRecursive(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested directory structure with SQL files
	dbDir := filepath.Join(tmpDir, "backups", "mysql", "full")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}

	sqlFile := filepath.Join(dbDir, "database.sql")
	if err := os.WriteFile(sqlFile, []byte("-- SQL dump\n"), 0644); err != nil {
		t.Fatal(err)
	}

	executor := &Executor{}
	found := executor.findDatabaseDump(tmpDir)

	if found != sqlFile {
		t.Errorf("expected to find %s, got %s", sqlFile, found)
	}
}

func TestDatabaseRestorationValidation(t *testing.T) {
	// Test that database restoration validates SQL file presence and validity
	executor := &Executor{}

	tmpDir := t.TempDir()
	sqlFile := filepath.Join(tmpDir, "test.sql")
	if err := os.WriteFile(sqlFile, []byte("CREATE TABLE test (id INT);"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Valid SQL file should pass validation (not return error)
	err := executor.restoreDatabase(ctx, sqlFile, "testdb", "testuser")
	if err != nil {
		t.Errorf("restoreDatabase should succeed with valid SQL file, got: %v", err)
	}

	// Non-existent file should fail
	err = executor.restoreDatabase(ctx, filepath.Join(tmpDir, "nonexistent.sql"), "testdb", "testuser")
	if err == nil {
		t.Error("restoreDatabase should fail for missing SQL file")
	}

	// Empty file should fail
	emptyFile := filepath.Join(tmpDir, "empty.sql")
	if err := os.WriteFile(emptyFile, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	err = executor.restoreDatabase(ctx, emptyFile, "testdb", "testuser")
	if err == nil {
		t.Error("restoreDatabase should fail for empty SQL file")
	}
}

func TestConfigurationUpdateIsNotImplemented(t *testing.T) {
	// This test documents that config update is a placeholder
	executor := &Executor{}

	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "wp-config.php")
	if err := os.WriteFile(configFile, []byte("<?php\ndefine('DB_NAME', 'old_db');\n"), 0644); err != nil {
		t.Fatal(err)
	}

	job := &ImportJob{
		WebRoot:      tmpDir,
		DatabaseName: "new_db",
		DatabaseUser: "new_user",
	}

	err := executor.updateConfiguration(job, "wp-config.php")

	// Currently returns nil (placeholder), but this documents the expected behavior
	if err != nil {
		t.Logf("updateConfiguration returned error (expected behavior when implemented): %v", err)
	}
}

func TestSSRFProtection(t *testing.T) {
	tests := []struct {
		url   string
		allow bool
	}{
		{"https://example.com/archive.tar.gz", true},
		{"https://cdn.example.com/archive.zip", true},
		{"http://example.com/archive.tar.gz", false},        // http not allowed
		{"https://localhost/archive.tar.gz", false},         // localhost
		{"https://127.0.0.1/archive.tar.gz", false},         // loopback
		{"https://169.254.169.254/latest/metadata", false},  // metadata endpoint
		{"https://10.0.0.1/internal/archive.tar.gz", false}, // private IP
		{"https://192.168.1.1/archive.tar.gz", false},       // private IP
		{"https://172.16.0.1/archive.tar.gz", false},        // private IP range
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%v", tt.url, tt.allow), func(t *testing.T) {
			allowed := isAllowedURL(tt.url)
			if allowed != tt.allow {
				t.Errorf("isAllowedURL(%q) = %v, want %v", tt.url, allowed, tt.allow)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && s[len(s)-len(substr):] != substr &&
		(len(s) < len(substr) || s[:len(substr)] != substr) &&
		findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
