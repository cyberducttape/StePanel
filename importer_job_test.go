package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestArchiveImportJobDoesNotPersistWebRootAuthority(t *testing.T) {
	payload, err := json.Marshal(durableArchiveImportRequest{
		ArchiveURL: "https://example.test/site.tar.gz",
		ConfigPath: "wp-config.php",
		SiteName:   "example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(`"web_root"`)) {
		t.Fatalf("archive job payload persisted deployment authority: %s", payload)
	}
}
