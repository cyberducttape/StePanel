package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/itchyitchy123/StePanel/internal/audit"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DRCheck struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Path     string `json:"path,omitempty"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
}

type DRManifest struct {
	Version   int       `json:"version"`
	Generated time.Time `json:"generated_at"`
	Checks    []DRCheck `json:"checks"`
	Notes     []string  `json:"notes"`
}

func controlPlaneDRChecks(cfg Config) []DRCheck {
	checks := []DRCheck{}
	addFile := func(name, category, path string, required bool) {
		check := DRCheck{Name: name, Category: category, Path: path, Status: "missing", Detail: "not found"}
		info, err := os.Lstat(path)
		if err == nil && !info.Mode().IsRegular() {
			check.Status, check.Detail = "unsafe", "must be a regular file"
		} else if err == nil {
			check.Status, check.Detail = "present", "regular file available for backup"
			if info.Mode().Perm()&0077 != 0 {
				check.Status, check.Detail = "warning", "present but group/world permissions are too broad"
				if required {
					check.Status, check.Detail = "unsafe", "required recovery artifact has group/world permissions"
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			check.Status, check.Detail = "error", err.Error()
		}
		if !required && check.Status == "missing" {
			check.Status, check.Detail = "optional-missing", "not configured; regenerate or configure when used"
		}
		checks = append(checks, check)
	}
	addFile("runtime configuration", "preserve", "/etc/ste-panel.env", false)
	addFile("audit log", "preserve", cfg.AuditLog, true)
	addFile("audit continuity state", "preserve", cfg.AuditLog+".state", true)
	addFile("audit HMAC key", "preserve", audit.AuditKeyPath, true)
	if drPathConfigured(cfg.ControlPlaneDB) {
		addFile("legacy job state", "preserve-or-regenerate", cfg.JobState, false)
	} else {
		addFile("job state", "preserve", cfg.JobState, true)
	}
	addFile("control-plane database", "preserve", cfg.ControlPlaneDB, true)
	addFile("session state", "preserve-or-regenerate", cfg.SessionState, false)
	addFile("account state", "preserve", cfg.AccountState, false)
	addFile("environment state", "preserve", cfg.EnvironmentState, false)
	addFile("Redis allocation state", "preserve", cfg.RedisState, false)
	addFile("Git known-host trust", "preserve-or-review", "/etc/stepanel/git-known-hosts", false)
	addFile("rclone configuration", "external dependency", os.Getenv("RCLONE_CONFIG"), false)
	if os.Getenv("RCLONE_CONFIG") == "" {
		checks[len(checks)-1].Status = "external"
		checks[len(checks)-1].Detail = "path is provider-managed; verify rclone config and credentials separately"
	}
	checks = append(checks,
		DRCheck{Name: "Git deploy keys", Category: "regenerate-or-preserve", Path: "/etc/stepanel/git-keys", Status: "operator-review", Detail: "private keys are not copied by this check; preserve only after access review, otherwise regenerate per site"},
		DRCheck{Name: "privileged helpers and systemd units", Category: "regenerate", Status: "regenerate", Detail: "restore from the verified release package and installer rather than treating host units as panel state"},
		DRCheck{Name: "site data and backups", Category: "preserve", Path: cfg.WebRoot, Status: "operator-review", Detail: "include site trees, verified backups, database server data, and off-site copies in the host DR plan"},
	)
	return checks
}

func runDRCheck(cfg Config) error {
	manifest := DRManifest{Version: 1, Generated: time.Now().UTC(), Checks: controlPlaneDRChecks(cfg), Notes: []string{
		"This manifest inventories recovery obligations; it does not copy or expose secret values.",
		"Preserve encryption/signing keys with their ciphertext or signed artifacts.",
		"Deploy keys may be regenerated after recovery if provider access is intentionally re-established.",
		"Remote audit anchoring remains a planned integration; control-plane backup and dry-run validation are provided by the CLI.",
	}}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stdout, string(data))
	for _, check := range manifest.Checks {
		if check.Category == "preserve" && (check.Status == "missing" || check.Status == "unsafe" || check.Status == "error") {
			return fmt.Errorf("DR check failed: %s is %s", check.Name, check.Status)
		}
	}
	return nil
}

func drPathConfigured(path string) bool { return strings.TrimSpace(path) != "" && filepath.IsAbs(path) }
