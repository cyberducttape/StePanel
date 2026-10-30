package stepanel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	h "github.com/cyberducttape/StePanel/internal/helper"
)

type SSHServerStatus struct {
	Server      string `json:"server"`
	Reachable   bool   `json:"reachable"`
	Hostname    string `json:"hostname,omitempty"`
	OS          string `json:"os,omitempty"`
	FailedUnits string `json:"failed_units,omitempty"`
	Disk        string `json:"disk,omitempty"`
	Error       string `json:"error,omitempty"`
}

type SSHActionResult struct {
	Server      string    `json:"server"`
	Action      string    `json:"action"`
	Service     string    `json:"service,omitempty"`
	CompletedAt time.Time `json:"completed_at"`
}

func configuredSSHServers() []string {
	value := os.Getenv("STEPANEL_SSH_SERVERS")
	if value == "" {
		return nil
	}
	seen := map[string]bool{}
	result := []string{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] || !sshAliasPattern.MatchString(item) {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

var sshAliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:-]{0,127}$`)

const (
	sshInventoryConcurrency  = 4
	sshInventoryTotalTimeout = 30 * time.Second
	sshInventoryHostTimeout  = 15 * time.Second
	sshInventoryStdoutLimit  = 16 << 10
	sshInventoryStderrLimit  = 4 << 10
)

// inspectSSHServerFunc is replaced in tests to simulate slow or failing hosts.
var inspectSSHServerFunc = inspectSSHServer

// sshInventory inspects every configured server concurrently, each under its
// own deadline, so one slow host cannot consume the time the others need.
// The whole inventory is bound to the request: a disconnected client cancels
// outstanding inspections. Every server gets a result; unreachable servers
// are also summarized in warnings.
func (a *App) sshInventory(w http.ResponseWriter, r *http.Request) {
	servers := configuredSSHServers()
	if len(servers) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"servers": []SSHServerStatus{}, "warnings": []string{"STEPANEL_SSH_SERVERS is not configured"}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sshInventoryTotalTimeout)
	defer cancel()
	results := make([]SSHServerStatus, len(servers))
	slots := make(chan struct{}, sshInventoryConcurrency)
	var wg sync.WaitGroup
	for i, server := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				results[i] = SSHServerStatus{Server: server, Error: "SSH inspection was not started before the inventory deadline"}
				return
			}
			hostCtx, hostCancel := context.WithTimeout(ctx, sshInventoryHostTimeout)
			defer hostCancel()
			results[i] = inspectSSHServerFunc(hostCtx, server)
		}()
	}
	wg.Wait()
	warnings := []string{}
	for _, result := range results {
		if !result.Reachable {
			warnings = append(warnings, result.Server+": "+result.Error)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": results, "warnings": warnings, "time": time.Now().UTC()})
}

func (a *App) sshAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var in struct{ Server, Action, Service string }
	if err := decodeJSON(w, r, 4096, &in); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	in.Server = strings.TrimSpace(in.Server)
	in.Action = strings.TrimSpace(in.Action)
	in.Service = strings.TrimSpace(in.Service)
	allowed := false
	for _, server := range configuredSSHServers() {
		if server == in.Server {
			allowed = true
			break
		}
	}
	if !allowed || (in.Action != "reboot" && in.Action != "restart-service") || (in.Action == "restart-service" && !sshServicePattern.MatchString(in.Service)) {
		http.Error(w, "invalid SSH server, action, or service", 422)
		return
	}
	operationKey, err := requestOperationKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	job, err := a.enqueueCloudJob(durableCloudRequest{Operation: "ssh", Provider: "ssh", Action: in.Action, ID: in.Server, Service: in.Service, Actor: a.Auth.UsernameForRequest(r)}, operationKey)
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "could not persist SSH job", 500)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID})
}

var sshServicePattern = regexp.MustCompile(`^[a-zA-Z0-9@_.:-]{1,80}$`)

func executeSSHAction(ctx context.Context, server, action, service string) error {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8", server, "sudo", "--non-interactive"}
	if action == "reboot" {
		args = append(args, "systemctl", "reboot")
	} else {
		args = append(args, "systemctl", "restart", service)
	}
	out, err := runBoundedCommand(ctx, exec.CommandContext(ctx, "ssh", args...))
	if err != nil {
		return fmt.Errorf("SSH action failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func inspectSSHServer(ctx context.Context, server string) SSHServerStatus {
	status := SSHServerStatus{Server: server}
	command := `printf '%s\n' "$(hostname)"; . /etc/os-release 2>/dev/null && printf '%s\n' "${PRETTY_NAME:-unknown}" || printf '%s\n' unknown; systemctl --failed --no-legend --plain 2>/dev/null | wc -l; df -P -h / 2>/dev/null | tail -n 1`
	// ssh joins the remote argv with spaces for the remote login shell, so
	// the script must arrive as one quoted word or that shell re-splits it.
	cmd := h.NewCommand(ctx, "ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8", server, "sh", "-c", sshRemoteQuote(command))
	out, stderr, err := h.RunCappedSeparate(ctx, cmd, sshInventoryStdoutLimit, sshInventoryStderrLimit)
	if err != nil {
		status.Error = fmt.Sprintf("SSH inspection failed: %v", err)
		if detail := strings.TrimSpace(string(stderr)); detail != "" {
			status.Error += ": " + detail
		}
		return status
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 4 {
		status.Error = "SSH inspection returned incomplete data"
		return status
	}
	status.Reachable = true
	status.Hostname = lines[0]
	status.OS = lines[1]
	status.FailedUnits = lines[2]
	status.Disk = lines[3]
	return status
}

// sshRemoteQuote single-quotes value as one POSIX shell word.
func sshRemoteQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func validateSSHServers() error {
	for _, value := range strings.Split(os.Getenv("STEPANEL_SSH_SERVERS"), ",") {
		value = strings.TrimSpace(value)
		if value != "" && !sshAliasPattern.MatchString(value) {
			return errors.New("STEPANEL_SSH_SERVERS contains an invalid host alias")
		}
	}
	return nil
}
