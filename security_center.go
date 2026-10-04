package stepanel

import (
	"net/http"
	"syscall"
	"time"
)

type HostSecurityCenter struct {
	Checks      []SecurityCheck   `json:"checks"`
	Services    map[string]string `json:"services"`
	Disk        DiskPosture       `json:"disk"`
	Backups     BackupPosture     `json:"backups"`
	GeneratedAt time.Time         `json:"generated_at"`
}
type DiskPosture struct {
	Path        string `json:"path"`
	FreeBytes   uint64 `json:"free_bytes"`
	InodesFree  uint64 `json:"inodes_free"`
	InodesTotal uint64 `json:"inodes_total"`
	Status      string `json:"status"`
}
type BackupPosture struct {
	Schedules           int        `json:"schedules"`
	OldestSuccess       *time.Time `json:"oldest_success,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
}

func (a *App) securityCenter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator access required", 403)
		return
	}
	d := DiskPosture{Path: a.Config.WebRoot, Status: "unknown"}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(a.Config.WebRoot, &stat); err == nil {
		d.FreeBytes = uint64(stat.Bavail) * uint64(stat.Bsize)
		d.InodesFree = stat.Ffree
		d.InodesTotal = stat.Files
		d.Status = "healthy"
		if d.InodesTotal > 0 && d.InodesFree*100/d.InodesTotal < 10 {
			d.Status = "warning"
		}
	}
	b := BackupPosture{}
	if a.Schedules != nil {
		items := a.Schedules.list()
		b.Schedules = len(items)
		for _, item := range items {
			b.ConsecutiveFailures += item.ConsecutiveFails
			if item.LastSuccess != nil && (b.OldestSuccess == nil || item.LastSuccess.Before(*b.OldestSuccess)) {
				t := *item.LastSuccess
				b.OldestSuccess = &t
			}
		}
	}
	writeJSON(w, 200, map[string]any{"checks": a.SecurityChecks(), "services": ServiceStatus(), "waf": a.WAFCapability(), "disk": d, "backups": b, "generated_at": time.Now().UTC()})
}
