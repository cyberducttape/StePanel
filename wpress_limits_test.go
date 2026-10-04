package stepanel

import (
	"encoding/base64"
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
	if err := applyWPressPackageMetadata(Config{WPCLI: helper}, filepath.Join(root, "site"), metadata); err != nil {
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

func TestValidateWPressTreeEnforcesEntryLimit(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "one"), "1")
	writeTestFile(t, filepath.Join(root, "two"), "2")
	if err := validateWPressTree(root, 2); err == nil {
		t.Fatal("oversized WPress entry count was accepted")
	}
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
