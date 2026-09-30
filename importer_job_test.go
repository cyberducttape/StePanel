package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/internal/importer"
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

type testArchiveInspector struct {
	inspection *importer.ArchiveInspection
	err        error
}

func (t testArchiveInspector) InspectArchive(context.Context, string, string) (*importer.ArchiveInspection, error) {
	return t.inspection, t.err
}

func TestArchiveInspectionDurableHandlerReturnsResultOutput(t *testing.T) {
	previous := newArchiveAnalyzer
	newArchiveAnalyzer = func() archiveInspector {
		return testArchiveInspector{inspection: &importer.ArchiveInspection{URL: "https://example.test/archive.zip", ConfigPath: "wp-config.php"}}
	}
	t.Cleanup(func() { newArchiveAnalyzer = previous })

	payload, err := json.Marshal(durableArchiveInspectionRequest{ArchiveURL: "https://example.test/archive.zip", ConfigPath: "wp-config.php"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := (&App{}).handleDurableJob(context.Background(), Job{Kind: "archive.inspect", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result["success"] != true || result["inspection"] == nil {
		t.Fatalf("durable inspection result = %#v", result)
	}
}

func TestArchiveInspectionFailureIsAJobFailureWithResultContext(t *testing.T) {
	previous := newArchiveAnalyzer
	newArchiveAnalyzer = func() archiveInspector {
		return testArchiveInspector{err: errors.New("archive is corrupt")}
	}
	t.Cleanup(func() { newArchiveAnalyzer = previous })

	payload, err := json.Marshal(durableArchiveInspectionRequest{ArchiveURL: "https://example.test/archive.zip", ConfigPath: "wp-config.php"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := (&App{}).handleDurableJob(context.Background(), Job{Kind: "archive.inspect", Payload: payload})
	if err == nil || !strings.Contains(err.Error(), "archive inspection failed: archive is corrupt") {
		t.Fatalf("inspection error = %v", err)
	}
	if !strings.Contains(string(output), `"success":false`) || !strings.Contains(string(output), "archive is corrupt") {
		t.Fatalf("inspection failure result = %s", output)
	}
}
