package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreImportedDatabaseUsesManagedHelperAndReturnsCleanup(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "db-helper")
	script := `#!/bin/sh
case "$1" in
  inventory) printf 'demo_db\tdemo\tdemo_user\t0\tutf8mb4\n' ;;
  provision|restore-dump|drop-managed) cat >/dev/null ;;
  *) exit 64 ;;
esac
`
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(root, "database.sql")
	if err := os.WriteFile(dump, []byte("CREATE TABLE smoke (id INT);\n"), 0600); err != nil {
		t.Fatal(err)
	}

	a := &App{Config: Config{DBCtl: helper, DBEngine: "mysql", WebRoot: root}}
	cleanup, err := a.restoreImportedDatabase(context.Background(), dump, "demo_db", "demo_user", "a-valid-password-123456", "demo")
	if err != nil {
		t.Fatalf("restoreImportedDatabase: %v", err)
	}
	if cleanup == nil {
		t.Fatal("restoreImportedDatabase returned no rollback cleanup")
	}
	if err := cleanup(); err != nil {
		t.Fatalf("database cleanup: %v", err)
	}
}
