// Package configdrift reads the result of the root configuration drift check
// (deploy/integrations/stepanel-driftctl), which compares the files the
// installer manages with the manifest recorded when it last committed.
package configdrift

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultStatusPath is where stepanel-config-drift.service writes its result.
const DefaultStatusPath = "/var/lib/stepanel-drift/status"

// StaleAfter is how old a result may be before the check itself is reported
// as not running (the timer runs hourly with up to 5 minutes of jitter).
const StaleAfter = 3 * time.Hour

const format = "stepanel-config-drift-v1"

// maxDriftEntries bounds what is parsed from the status file.
const maxDriftEntries = 1000

// Drift is one managed file that no longer matches the manifest.
type Drift struct {
	Path   string `json:"path"`
	Reason string `json:"reason"` // content, mode, owner, type, missing, unreadable
}

// Status is the latest drift check result.
type Status struct {
	CheckedAt       time.Time `json:"checked_at"`
	ManifestPresent bool      `json:"manifest_present"`
	Files           int       `json:"files"`
	Drifted         int       `json:"drifted"`
	Drift           []Drift   `json:"drift,omitempty"`
}

// ErrNoResult reports that the drift check has never written a result.
var ErrNoResult = errors.New("configuration drift check has not run")

// Read reads the status file at path.
func Read(path string) (Status, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Status{}, ErrNoResult
		}
		return Status{}, fmt.Errorf("read configuration drift status: %w", err)
	}
	defer file.Close()
	return Parse(io.LimitReader(file, 1<<20))
}

// Parse parses a status file.
func Parse(r io.Reader) (Status, error) {
	var status Status
	seen := map[string]bool{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "drift\t"); ok {
			path, reason, ok := strings.Cut(rest, "\t")
			if !ok || !strings.HasPrefix(path, "/") || reason == "" {
				return Status{}, fmt.Errorf("malformed drift line %q", line)
			}
			if len(status.Drift) < maxDriftEntries {
				status.Drift = append(status.Drift, Drift{Path: path, Reason: reason})
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Status{}, fmt.Errorf("malformed status line %q", line)
		}
		seen[key] = true
		var err error
		switch key {
		case "format":
			if value != format {
				return Status{}, fmt.Errorf("unsupported drift status format %q", value)
			}
		case "checked_unix":
			var unix int64
			unix, err = strconv.ParseInt(value, 10, 64)
			status.CheckedAt = time.Unix(unix, 0).UTC()
		case "manifest":
			status.ManifestPresent = value == "present"
		case "files":
			status.Files, err = strconv.Atoi(value)
		case "drifted":
			status.Drifted, err = strconv.Atoi(value)
		}
		if err != nil {
			return Status{}, fmt.Errorf("invalid %s in drift status: %w", key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return Status{}, err
	}
	for _, key := range []string{"format", "checked_unix", "manifest", "drifted"} {
		if !seen[key] {
			return Status{}, fmt.Errorf("drift status has no %s", key)
		}
	}
	if status.Drifted < 0 || status.Files < 0 || (status.Drifted != len(status.Drift) && len(status.Drift) < maxDriftEntries) {
		return Status{}, fmt.Errorf("drift status reports %d drifted files but lists %d", status.Drifted, len(status.Drift))
	}
	return status, nil
}

// Health summarizes a result for the operational health endpoint: ok, and a
// detail an operator can act on.
func Health(status Status, err error, now time.Time) (bool, string) {
	switch {
	case errors.Is(err, ErrNoResult):
		return false, "the configuration drift check has not run; check stepanel-config-drift.timer"
	case err != nil:
		return false, err.Error()
	case !status.ManifestPresent:
		return false, "no managed-file manifest; re-run the installer to record one"
	case now.Sub(status.CheckedAt) > StaleAfter:
		return false, fmt.Sprintf("the last configuration drift check ran %s ago; check stepanel-config-drift.timer", now.Sub(status.CheckedAt).Round(time.Minute))
	case status.Drifted > 0:
		listed := make([]string, 0, 5)
		for i, drift := range status.Drift {
			if i == 5 {
				listed = append(listed, fmt.Sprintf("and %d more", status.Drifted-5))
				break
			}
			listed = append(listed, drift.Path+" ("+drift.Reason+")")
		}
		return false, fmt.Sprintf("%d managed file(s) changed since the installer recorded them: %s; restore them or re-run the installer", status.Drifted, strings.Join(listed, ", "))
	}
	return true, fmt.Sprintf("%d managed files match the installer's manifest", status.Files)
}
