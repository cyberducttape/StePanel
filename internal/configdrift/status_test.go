package configdrift

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const clean = "format=stepanel-config-drift-v1\nchecked_unix=1764000000\nmanifest=present\nmanifest_unix=1763990000\nfiles=33\ndrifted=0\n"

func TestParseCleanAndDriftedStatus(t *testing.T) {
	status, err := Parse(strings.NewReader(clean))
	if err != nil {
		t.Fatal(err)
	}
	if !status.ManifestPresent || status.Files != 33 || status.Drifted != 0 || !status.CheckedAt.Equal(time.Unix(1764000000, 0)) {
		t.Fatalf("clean status = %+v", status)
	}
	drifted := strings.Replace(clean, "drifted=0\n", "drifted=2\ndrift\t/etc/sudoers.d/stepanel\tcontent\ndrift\t/etc/stepanel-audit.key\tmode\n", 1)
	status, err = Parse(strings.NewReader(drifted))
	if err != nil {
		t.Fatal(err)
	}
	if status.Drifted != 2 || status.Drift[0] != (Drift{Path: "/etc/sudoers.d/stepanel", Reason: "content"}) || status.Drift[1].Reason != "mode" {
		t.Fatalf("drifted status = %+v", status)
	}
}

func TestParseRejectsMalformedStatus(t *testing.T) {
	for name, input := range map[string]string{
		"wrong format":   strings.Replace(clean, "v1", "v9", 1),
		"no drifted":     strings.Replace(clean, "drifted=0\n", "", 1),
		"bad number":     strings.Replace(clean, "files=33", "files=many", 1),
		"count mismatch": strings.Replace(clean, "drifted=0", "drifted=2", 1),
		"relative path":  clean + "drift\tetc/x\tcontent\n",
		"garbage":        clean + "nonsense\n",
	} {
		if _, err := Parse(strings.NewReader(input)); err == nil {
			t.Errorf("%s: malformed status was accepted", name)
		}
	}
}

func TestHealthReportsEachCondition(t *testing.T) {
	now := time.Unix(1764000000, 0).Add(10 * time.Minute)
	status, _ := Parse(strings.NewReader(clean))
	if ok, detail := Health(status, nil, now); !ok || !strings.Contains(detail, "33 managed files match") {
		t.Fatalf("clean health = %v %q", ok, detail)
	}
	if ok, detail := Health(status, nil, now.Add(4*time.Hour)); ok || !strings.Contains(detail, "stepanel-config-drift.timer") {
		t.Fatalf("stale health = %v %q", ok, detail)
	}
	if ok, detail := Health(Status{}, ErrNoResult, now); ok || !strings.Contains(detail, "has not run") {
		t.Fatalf("never-run health = %v %q", ok, detail)
	}
	missing := status
	missing.ManifestPresent = false
	if ok, detail := Health(missing, nil, now); ok || !strings.Contains(detail, "re-run the installer") {
		t.Fatalf("missing-manifest health = %v %q", ok, detail)
	}
	drifted := status
	for i := 0; i < 7; i++ {
		drifted.Drift = append(drifted.Drift, Drift{Path: "/etc/f" + string(rune('a'+i)), Reason: "content"})
	}
	drifted.Drifted = 7
	ok, detail := Health(drifted, nil, now)
	if ok || !strings.Contains(detail, "7 managed file(s) changed") || !strings.Contains(detail, "/etc/fa (content)") || !strings.Contains(detail, "and 2 more") {
		t.Fatalf("drifted health = %v %q", ok, detail)
	}
}

func TestReadDistinguishesNeverRunFromUnreadable(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(filepath.Join(dir, "absent")); !errors.Is(err, ErrNoResult) {
		t.Fatalf("absent status error = %v", err)
	}
	path := filepath.Join(dir, "status")
	if err := os.WriteFile(path, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, err := Read(path); err != nil || status.Files != 33 {
		t.Fatalf("read status = %+v, %v", status, err)
	}
}
