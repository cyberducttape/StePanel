package stepanel

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyWPressPackageMetadataUsesWPCLI(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "wpcli.log")
	helper := filepath.Join(root, "wp")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_WPCLI_LOG\"\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_WPCLI_LOG", logPath)
	metadata := wpressPackageMetadata{PluginsPresent: true, Plugins: []string{"akismet/akismet.php"}, TemplatePresent: true, Template: "twentytwentyfour", StylesheetPresent: true, Stylesheet: "twentytwentyfour"}
	if err := applyWPressPackageMetadata(Config{WPCLI: helper, WebRoot: filepath.Join(root, "www")}, "account", metadata); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"option update active_plugins", "akismet/akismet.php", "--format=json", "option update template twentytwentyfour", "option update stylesheet twentytwentyfour"} {
		if !strings.Contains(text, want) {
			t.Fatalf("WP-CLI invocation %q missing from %q", want, text)
		}
	}
}

func TestReadWPressPackageMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package.json")
	htaccess := base64.StdEncoding.EncodeToString([]byte("RewriteEngine On\n"))
	content := fmt.Sprintf(`{"Plugins":["akismet/akismet.php"],"Template":"twentytwentyfour","Stylesheet":"twentytwentyfour","Server":{".htaccess":%q}}`, htaccess)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, err := readWPressPackageMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if !metadata.PluginsPresent || len(metadata.Plugins) != 1 || metadata.Plugins[0] != "akismet/akismet.php" || metadata.Template != "twentytwentyfour" || metadata.Stylesheet != "twentytwentyfour" || string(metadata.HTAccess) != "RewriteEngine On\n" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestReadWPressPackageMetadataRejectsUnsafePlugin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package.json")
	if err := os.WriteFile(path, []byte(`{"Plugins":["../evil.php"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWPressPackageMetadata(path); err == nil {
		t.Fatal("unsafe plugin path was accepted")
	}
}

func TestInspectCPMoveRejectsMissingMetadata(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "empty-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := inspectCPMove(file, nil, 10); err == nil {
		t.Fatal("missing archive metadata was accepted")
	}
}

func TestInspectWPressArchiveAcceptsSafeRecordsAndSumsExpandedBytes(t *testing.T) {
	archive := writeWPressFixture(t,
		wpressFixtureEntry{prefix: "wordpress", name: "database.sql", data: []byte("sql")},
		wpressFixtureEntry{prefix: "wordpress/wp-content", name: "index.php", data: []byte("php!!")},
	)
	expanded, err := inspectWPressArchive(archive, 10)
	if err != nil {
		t.Fatalf("inspect valid archive: %v", err)
	}
	if expanded != uint64(len("sql")+len("php!!")) {
		t.Fatalf("expanded size = %d, want %d", expanded, len("sql")+len("php!!"))
	}
}

func TestInspectWPressArchiveRejectsTraversalBeforeExtraction(t *testing.T) {
	for name, entry := range map[string]wpressFixtureEntry{
		"parent component in name":   {name: "../escape", data: []byte("x")},
		"parent component in prefix": {prefix: "../../outside", name: "escape", data: []byte("x")},
		"absolute name":              {name: "/tmp/escape", data: []byte("x")},
		"absolute prefix":            {prefix: "/tmp", name: "escape", data: []byte("x")},
	} {
		t.Run(name, func(t *testing.T) {
			archive := writeWPressFixture(t, entry)
			if _, err := inspectWPressArchive(archive, 10); err == nil {
				t.Fatal("unsafe archive path passed pre-extraction validation")
			}
		})
	}
}

func TestRestoreWPressRejectsTraversalBeforeStartingExtractor(t *testing.T) {
	root := t.TempDir()
	importRoot := filepath.Join(root, "imports")
	if err := os.MkdirAll(importRoot, 0700); err != nil {
		t.Fatal(err)
	}
	archive := writeWPressFixture(t, wpressFixtureEntry{name: "../../escape", data: []byte("x")})
	marker := filepath.Join(root, "extractor-ran")
	extractor := filepath.Join(root, "extractor")
	script := fmt.Sprintf("#!/bin/sh\n: > %q\n", marker)
	if err := os.WriteFile(extractor, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ImportRoot: importRoot, MaxUpload: 1 << 20, WPressExtract: extractor, WPCLI: "/bin/true"}
	_, err := RestoreWPress(cfg, archive, AuthorizedSite{site: "account"}, "db", "user", "aValidPassword123", "", "wp_", false)
	if err == nil {
		t.Fatal("path-traversing WPress archive was accepted")
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("external extractor ran before archive validation: stat marker error = %v", statErr)
	}
}

func TestInspectWPressArchiveRejectsTruncationAndEntryOverflow(t *testing.T) {
	t.Run("truncated payload", func(t *testing.T) {
		archive := writeWPressFixture(t, wpressFixtureEntry{name: "database.sql", data: []byte("sql")})
		if err := os.Truncate(archive, int64(wpressHeaderSize+2)); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectWPressArchive(archive, 10); err == nil {
			t.Fatal("truncated WPress entry was accepted")
		}
	})
	t.Run("entry count", func(t *testing.T) {
		archive := writeWPressFixture(t,
			wpressFixtureEntry{name: "one", data: []byte("1")},
			wpressFixtureEntry{name: "two", data: []byte("2")},
		)
		if _, err := inspectWPressArchive(archive, 1); err == nil {
			t.Fatal("WPress entry count above the limit was accepted")
		}
	})
	t.Run("duplicate output path", func(t *testing.T) {
		archive := writeWPressFixture(t,
			wpressFixtureEntry{name: "database.sql", data: []byte("first")},
			wpressFixtureEntry{name: "database.sql", data: []byte("second")},
		)
		if _, err := inspectWPressArchive(archive, 10); err == nil {
			t.Fatal("duplicate WPress output path was accepted")
		}
	})
	t.Run("expanded size", func(t *testing.T) {
		header := make([]byte, wpressHeaderSize)
		copy(header[:wpressNameEnd], "large")
		copy(header[wpressNameEnd:wpressSizeEnd], fmt.Sprintf("%014d", uint64(maxBackupBytes)+1))
		copy(header[wpressPrefixOffset:], "wordpress")
		archive := filepath.Join(t.TempDir(), "oversized.wpress")
		if err := os.WriteFile(archive, append(header, make([]byte, wpressHeaderSize)...), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectWPressArchive(archive, 10); err == nil {
			t.Fatal("WPress expanded size above the limit was accepted")
		}
	})
}

func TestValidateWPressTreeEnforcesEntryLimit(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "one"), "1")
	writeTestFile(t, filepath.Join(root, "two"), "2")
	if err := validateWPressTree(root, 2); err == nil {
		t.Fatal("oversized WPress entry count was accepted")
	}
}

func TestWPressRestoreAdmissionUsesExpandedTreeLimit(t *testing.T) {
	cfg := Config{ImportRoot: "/srv/imports", WebRoot: "/srv/www"}
	const expandedBytes = uint64(1234)
	demands := wpressRestoreDemands(cfg, expandedBytes)
	if len(demands) != 2 {
		t.Fatalf("got %d WPress capacity demands, want import and site staging", len(demands))
	}
	wantPaths := map[string]bool{
		cfg.ImportRoot:                      false,
		filepath.Join(cfg.WebRoot, "sites"): false,
	}
	for _, demand := range demands {
		if demand.Bytes != expandedBytes {
			t.Errorf("demand for %s = %d bytes, want inspected expanded size %d", demand.Path, demand.Bytes, expandedBytes)
		}
		if _, ok := wantPaths[demand.Path]; !ok {
			t.Errorf("unexpected WPress capacity path %q", demand.Path)
		} else {
			wantPaths[demand.Path] = true
		}
	}
	for path, seen := range wantPaths {
		if !seen {
			t.Errorf("missing WPress capacity reservation for %s", path)
		}
	}
}

type wpressFixtureEntry struct {
	prefix string
	name   string
	data   []byte
}

func writeWPressFixture(t *testing.T, entries ...wpressFixtureEntry) string {
	t.Helper()
	var archive []byte
	for _, entry := range entries {
		header := make([]byte, wpressHeaderSize)
		copy(header[:wpressNameEnd], entry.name)
		copy(header[wpressNameEnd:wpressSizeEnd], fmt.Sprintf("%014d", len(entry.data)))
		copy(header[wpressPrefixOffset:], entry.prefix)
		archive = append(archive, header...)
		archive = append(archive, entry.data...)
	}
	archive = append(archive, make([]byte, wpressHeaderSize)...)
	path := filepath.Join(t.TempDir(), "fixture.wpress")
	if err := os.WriteFile(path, archive, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateWPressTreeRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "unsafe")); err != nil {
		t.Fatal(err)
	}
	if err := validateWPressTree(root, 10); err == nil {
		t.Fatal("WPress symlink was accepted")
	}
}
