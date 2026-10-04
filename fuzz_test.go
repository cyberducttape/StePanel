package stepanel

import (
	"strings"
	"testing"
)

func FuzzSafeUser(f *testing.F) {
	for _, seed := range []string{"admin", "site-1", "../etc/passwd", "", "a/b", "site_😀"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		got := safeUser(value)
		if got == "" {
			return
		}
		if len(got) > 32 || strings.ContainsAny(got, "/\\"+string(rune(0))) {
			t.Fatalf("safeUser accepted unsafe value %q", got)
		}
		for _, r := range got {
			if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				t.Fatalf("safeUser accepted unexpected rune %q in %q", r, got)
			}
		}
	})
}

func FuzzValidBackupName(f *testing.F) {
	for _, seed := range []string{"site-20260910.tar.zst", "..", "", "../../backup", "backup_01"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if !validBackupName(value) {
			return
		}
		if len(value) > 160 || value == "." || value == ".." || strings.ContainsAny(value, "/\\"+string(rune(0))) {
			t.Fatalf("validBackupName accepted unsafe value %q", value)
		}
	})
}

func FuzzManagedDatabaseIdentifier(f *testing.F) {
	for _, seed := range []string{"site_db", "wordpress1", "bad-name", "", "../../db", "db\x00name"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if !validManagedDatabaseIdentifier(value, 64) {
			return
		}
		if len(value) > 64 || strings.ContainsAny(value, "/\\"+string(rune(0))) {
			t.Fatalf("database identifier accepted unsafe value %q", value)
		}
	})
}
