package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cyberducttape/StePanel/internal/doctor"
)

var migrationSSHHostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
var migrationSSHUserPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)

// This script is fixed and supplied on stdin; user input is passed only as
// validated ssh arguments. It deliberately gathers facts, not arbitrary files
// or command output, so a source scan cannot become a remote command runner.
const migrationInspectionScript = `set -u
emit() { printf '%s\t%s\n' "$1" "$2"; }
emit HOSTNAME "$(hostname 2>/dev/null || true)"
if [ -r /etc/os-release ]; then . /etc/os-release; emit OS_NAME "${NAME:-}"; emit OS_VERSION "${VERSION_ID:-}"; emit OS_ID "${ID:-}"; fi
emit KERNEL "$(uname -r 2>/dev/null || true)"
emit ARCH "$(uname -m 2>/dev/null || true)"
if command -v php >/dev/null 2>&1; then emit PHP_VERSION "$(php -r 'echo PHP_VERSION;' 2>/dev/null || true)"; emit PHP_EXTENSIONS "$(php -m 2>/dev/null | tr '\n' ',' || true)"; fi
if command -v mysql >/dev/null 2>&1; then emit DB_VERSION "$(mysql --version 2>/dev/null || true)"; fi
if command -v psql >/dev/null 2>&1; then emit PG_VERSION "$(psql --version 2>/dev/null || true)"; fi
df -Pk / 2>/dev/null | awk 'NR==2 { printf "DISK_TOTAL_KB\t%s\nDISK_AVAILABLE_KB\t%s\n", $2, $4 }'
if command -v nproc >/dev/null 2>&1; then emit CPU_CORES "$(nproc 2>/dev/null || true)"; fi
if command -v free >/dev/null 2>&1; then free -m 2>/dev/null | awk 'NR==2 { printf "MEMORY_MB\t%s\n", $2 }'; fi
for root in /var/www /home; do
  [ -d "$root" ] || continue
  find "$root" -maxdepth 7 -type f -name wp-config.php -print 2>/dev/null | head -100 | while IFS= read -r config; do
    site=${config%/wp-config.php}; emit WORDPRESS_ROOT "$site"; emit SITE_BYTES "$(du -sb "$site" 2>/dev/null | awk '{print $1}')"; emit SITE_FILES "$(find "$site" -type f 2>/dev/null | wc -l)"
  done
done
emit CRON_COUNT "$(crontab -l 2>/dev/null | grep -cve '^\s*$' || true)"
`

func inspectMigrationSource(ctx context.Context, req migrationAnalysisRequest) (doctor.ServerInventory, error) {
	if !migrationSSHHostPattern.MatchString(req.SourceSSHHost) || strings.HasPrefix(req.SourceSSHHost, "-") {
		return doctor.ServerInventory{}, fmt.Errorf("invalid source SSH host")
	}
	if !migrationSSHUserPattern.MatchString(req.SourceSSHUser) || req.SourceSSHPort < 1 || req.SourceSSHPort > 65535 {
		return doctor.ServerInventory{}, fmt.Errorf("invalid source SSH credentials")
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=yes", "-p", strconv.Itoa(req.SourceSSHPort)}
	cleanup := []string{}
	defer func() {
		for _, path := range cleanup {
			_ = os.Remove(path)
		}
	}()
	if req.SourceSSHKey != "" {
		key, err := base64.StdEncoding.DecodeString(req.SourceSSHKey)
		if err != nil || len(key) == 0 {
			return doctor.ServerInventory{}, fmt.Errorf("source_ssh_key must be base64")
		}
		file, err := os.CreateTemp("", "stepanel-migration-key-")
		if err != nil {
			return doctor.ServerInventory{}, err
		}
		path := file.Name()
		cleanup = append(cleanup, path)
		if err := file.Chmod(0600); err != nil {
			file.Close()
			return doctor.ServerInventory{}, err
		}
		if _, err := file.Write(key); err != nil {
			file.Close()
			return doctor.ServerInventory{}, err
		}
		if err := file.Close(); err != nil {
			return doctor.ServerInventory{}, err
		}
		args = append(args, "-i", path)
	}
	if req.SourceSSHKnownHosts != "" {
		file, err := os.CreateTemp("", "stepanel-migration-known-hosts-")
		if err != nil {
			return doctor.ServerInventory{}, err
		}
		path := file.Name()
		cleanup = append(cleanup, path)
		if err := file.Chmod(0600); err != nil {
			file.Close()
			return doctor.ServerInventory{}, err
		}
		if _, err := io.WriteString(file, req.SourceSSHKnownHosts); err != nil {
			file.Close()
			return doctor.ServerInventory{}, err
		}
		if err := file.Close(); err != nil {
			return doctor.ServerInventory{}, err
		}
		args = append(args, "-o", "UserKnownHostsFile="+path)
	}
	args = append(args, req.SourceSSHUser+"@"+req.SourceSSHHost, "sh", "-s")
	output, err := runBoundedCommandInput(ctx, exec.CommandContext(ctx, "ssh", args...), strings.NewReader(migrationInspectionScript))
	if err != nil {
		return doctor.ServerInventory{}, fmt.Errorf("ssh inspection failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return parseMigrationInventory(string(output), req.SourceSSHHost)
}

func parseMigrationInventory(output, fallbackHost string) (doctor.ServerInventory, error) {
	if !strings.Contains(output, "\t") {
		return doctor.ServerInventory{Hostname: fallbackHost}, fmt.Errorf("source returned no recognizable inventory data")
	}
	inv := doctor.ServerInventory{Hostname: fallbackHost, ScanTime: time.Now().UTC()}
	var roots []string
	var sizes []int64
	var files []int
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], strings.TrimSpace(parts[1])
		switch key {
		case "HOSTNAME":
			inv.Hostname = value
		case "OS_NAME":
			inv.OS.Name = value
		case "OS_VERSION":
			inv.OS.Version = value
		case "OS_ID":
			inv.OS.DistributorID = value
		case "KERNEL":
			inv.OS.KernelVersion = value
		case "ARCH":
			inv.OS.Architecture = value
		case "PHP_VERSION":
			inv.PHP.Version = value
		case "PHP_EXTENSIONS":
			for _, ext := range strings.Split(value, ",") {
				if ext != "" {
					inv.PHP.Extensions = append(inv.PHP.Extensions, strings.ToLower(ext))
				}
			}
		case "DB_VERSION":
			inv.Database.Type, inv.Database.Version = "MySQL", value
		case "PG_VERSION":
			inv.Database.Type, inv.Database.Version = "PostgreSQL", value
		case "DISK_TOTAL_KB":
			inv.SystemResources.TotalDiskGB = parseDoctorInt(value) / (1024 * 1024)
		case "DISK_AVAILABLE_KB":
			inv.SystemResources.AvailableDiskGB = parseDoctorInt(value) / (1024 * 1024)
		case "CPU_CORES":
			inv.SystemResources.CPUCores = parseDoctorIntAsInt(value)
		case "MEMORY_MB":
			inv.SystemResources.TotalMemoryGB = parseDoctorIntAsInt(value) / 1024
		case "WORDPRESS_ROOT":
			roots = append(roots, value)
		case "SITE_BYTES":
			sizes = append(sizes, parseDoctorInt(value))
		case "SITE_FILES":
			files = append(files, parseDoctorIntAsInt(value))
		case "CRON_COUNT":
			inv.CronJobs = parseDoctorIntAsInt(value)
		}
	}
	for i, root := range roots {
		size, count := int64(0), 0
		if i < len(sizes) {
			size = sizes[i]
		}
		if i < len(files) {
			count = files[i]
		}
		inv.Sites = append(inv.Sites, doctor.SiteInfo{Domain: root, DocumentRoot: root, DiskUsageMB: size / (1024 * 1024), FileCount: count, Application: "WordPress"})
	}
	if inv.Hostname == "" && inv.OS.Name == "" && inv.PHP.Version == "" && inv.Database.Type == "" {
		return inv, fmt.Errorf("source returned no recognizable inventory data")
	}
	return inv, nil
}

func parseDoctorInt(value string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return n
}

func parseDoctorIntAsInt(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return n
}

func (a *App) localMigrationInventory() doctor.ServerInventory {
	inv := doctor.ServerInventory{ScanTime: time.Now().UTC(), Database: doctor.DatabaseSystem{Type: a.Config.DBEngine, Version: a.Config.DBVersion, Reachable: true}}
	if inv.Database.Type == "mysql" {
		inv.Database.Type = "MySQL"
	}
	if inv.Database.Type == "mariadb" {
		inv.Database.Type = "MariaDB"
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(a.Config.WebRoot, &stat); err == nil {
		inv.SystemResources.TotalDiskGB = int64(stat.Blocks * uint64(stat.Bsize) / (1024 * 1024 * 1024))
		inv.SystemResources.AvailableDiskGB = int64(stat.Bavail * uint64(stat.Bsize) / (1024 * 1024 * 1024))
	}
	if inv.Hostname == "" {
		inv.Hostname, _ = os.Hostname()
	}
	return inv
}
