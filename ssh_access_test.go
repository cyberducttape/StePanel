package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSSHLabel(t *testing.T) {
	valid := []string{"laptop", "ci-key_1", "deploy.key"}
	for _, label := range valid {
		if !validateSSHLabel(label) {
			t.Errorf("validateSSHLabel(%q) = false, want true", label)
		}
	}
	invalid := []string{"", "has space", "has/slash", strings.Repeat("a", 65)}
	for _, label := range invalid {
		if validateSSHLabel(label) {
			t.Errorf("validateSSHLabel(%q) = true, want false", label)
		}
	}
}

func TestParseSSHKeyRejectsOptionsAndDSA(t *testing.T) {
	ed25519Key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILGGHud+PeTCPf04AEzm7tsgAAzjEG+0BzCWyJxwtSZ4 test@example.com"
	key, err := parseSSHKey(ed25519Key)
	if err != nil {
		t.Fatalf("parseSSHKey rejected a valid ed25519 key: %v", err)
	}
	if key.PublicKey != ed25519Key || key.Fingerprint == "" {
		t.Fatalf("unexpected parsed key: %#v", key)
	}

	dsaKey := "ssh-dss AAAAB3NzaC1kc3MAAACBALvN4JWa41CSq8IUgIeWpA8B6yDTDMKCFxYrbilroYV8ljvLWu5XOG8gIZORoEW9wmYr9y0aBP/s7pOxuUDQf0THa4TS1iywM1r/VoFCiZmu19qb/Th+LGEodyQ1flJaiozDbYTEbEpwEChNnGXTWUfPidQb8fzif3D9udC9ojTdAAAAFQD3bx9Z9bgESlImFBQmaA+hFqNWHQAAAIBqVmrF2sONtDYAv0E0oEFusZ7aYU+eFCvKBj1KyXs+mvAOaAKxYp9lfehvc+Yjb+GtSwXweleHE7u16W6wjrgLo02X8/uzOE8cL3bue/8keuwA9KXqceBYWOukuf1VsW6oO/zPSU65b2HrUEW7McVF2X6dXIFuY0wRqml96WENxAAAAIANAD46eejr9oexk/+HpkU7e1jJs4zR5its9NbxX3d4Ij3EYK8zqScSwCTk91U0uuersotZ3Z7MJ5/RQkJ8q+w3YkzF1lN6kQ/q/+USPO/V9OZqrgiA5ZXpQte7xkubliXz6zz8v4KU8li/VSw+50dT4msFPFeCwioy70nRaYGmyg== test@example.com"
	if _, err := parseSSHKey(dsaKey); err == nil {
		t.Fatal("parseSSHKey accepted a DSA key")
	}

	withOptions := `command="/bin/echo hi" ` + ed25519Key
	if _, err := parseSSHKey(withOptions); err == nil {
		t.Fatal("parseSSHKey accepted a key carrying its own options")
	}

	if _, err := parseSSHKey("not a key"); err == nil {
		t.Fatal("parseSSHKey accepted garbage input")
	}
}

func TestOpenSiteAccessStoreNormalizesLegacyEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	if err := os.WriteFile(path, []byte(`{"demo":{"site":"","sftp_enabled":true,"keys":[]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenSiteAccessStore(path)
	if err != nil {
		t.Fatal(err)
	}
	access := store.values["demo"]
	if access.Site != "demo" || access.State != "applied" {
		t.Fatalf("normalized access = %#v", access)
	}
}

func TestApplySiteAccessSendsOnlyValidatedPolicyAndKeysToHelper(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input")
	helper := filepath.Join(dir, "helper")
	script := "#!/bin/sh\nprintf '%s %s %s\\n' \"$2\" \"$3\" \"$4\" > \"" + inputPath + "\"\ncat >> \"" + inputPath + "\"\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{SiteCtl: helper}}
	access := SiteAccess{Site: "demo", SFTPEnabled: true, ShellEnabled: false, Keys: []SSHKey{{PublicKey: "ssh-ed25519 AAAA"}}}
	if err := app.applySiteAccess(context.Background(), access); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "demo 1 0") || !strings.Contains(string(data), "ssh-ed25519 AAAA") {
		t.Fatalf("helper input = %q", data)
	}
}
