package stepanel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfiguredSSHServers(t *testing.T) {
	t.Setenv("STEPANEL_SSH_SERVERS", "prod-a, prod-a,admin@example.com:2222, bad host")
	servers := configuredSSHServers()
	if len(servers) != 2 || servers[0] != "prod-a" || servers[1] != "admin@example.com:2222" {
		t.Fatalf("servers = %#v", servers)
	}
	if err := validateSSHServers(); err == nil {
		t.Fatal("invalid SSH alias accepted")
	}
}

func stubSSHInspection(t *testing.T, inspect func(context.Context, string) SSHServerStatus) {
	t.Helper()
	original := inspectSSHServerFunc
	inspectSSHServerFunc = inspect
	t.Cleanup(func() { inspectSSHServerFunc = original })
}

func decodeSSHInventory(t *testing.T, response *httptest.ResponseRecorder) ([]SSHServerStatus, []string) {
	t.Helper()
	var body struct {
		Servers  []SSHServerStatus `json:"servers"`
		Warnings []string          `json:"warnings"`
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Servers, body.Warnings
}

// A host that hangs until its deadline must not delay or fail the others,
// and every server keeps its configured position in the result.
func TestSSHInventoryReturnsPartialResultsAroundSlowHost(t *testing.T) {
	t.Setenv("STEPANEL_SSH_SERVERS", "fast-a,slow,fast-b")
	var active, peak atomic.Int32
	var hostsStarted atomic.Int32
	barrier := make(chan struct{})
	stubSSHInspection(t, func(ctx context.Context, server string) SSHServerStatus {
		n := active.Add(1)
		for {
			current := peak.Load()
			if n <= current || peak.CompareAndSwap(current, n) {
				break
			}
		}
		defer active.Add(-1)
		if hostsStarted.Add(1) == 3 {
			close(barrier)
		}
		select {
		case <-barrier:
		case <-ctx.Done():
			return SSHServerStatus{Server: server, Error: "SSH inspection failed: " + ctx.Err().Error()}
		}
		if server == "slow" {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("host inspection has no per-host deadline")
			}
			<-ctx.Done()
			return SSHServerStatus{Server: server, Error: "SSH inspection failed: " + ctx.Err().Error()}
		}
		return SSHServerStatus{Server: server, Reachable: true, Hostname: server}
	})
	request := httptest.NewRequest(http.MethodGet, "/api/ssh/inventory", nil)
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	response := httptest.NewRecorder()
	started := time.Now()
	(&App{}).sshInventory(response, request.WithContext(ctx))
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("inventory took %s; the request context did not bound it", elapsed)
	}
	servers, warnings := decodeSSHInventory(t, response)
	if len(servers) != 3 || servers[0].Server != "fast-a" || !servers[0].Reachable || servers[1].Server != "slow" || servers[1].Reachable || servers[2].Server != "fast-b" || !servers[2].Reachable {
		t.Fatalf("servers = %+v, want per-host results in configured order", servers)
	}
	if len(warnings) != 1 || warnings[0] == "" {
		t.Fatalf("warnings = %#v, want one for the slow host", warnings)
	}
	if peak.Load() < 2 {
		t.Fatal("servers were inspected serially")
	}
}

func TestSSHInventoryBoundsConcurrency(t *testing.T) {
	t.Setenv("STEPANEL_SSH_SERVERS", "a,b,c,d,e,f,g,h,i,j")
	var active, peak atomic.Int32
	stubSSHInspection(t, func(_ context.Context, server string) SSHServerStatus {
		n := active.Add(1)
		for {
			current := peak.Load()
			if n <= current || peak.CompareAndSwap(current, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		return SSHServerStatus{Server: server, Reachable: true}
	})
	response := httptest.NewRecorder()
	(&App{}).sshInventory(response, httptest.NewRequest(http.MethodGet, "/api/ssh/inventory", nil))
	servers, warnings := decodeSSHInventory(t, response)
	if len(servers) != 10 || len(warnings) != 0 {
		t.Fatalf("servers = %d, warnings = %#v", len(servers), warnings)
	}
	if peak.Load() > sshInventoryConcurrency {
		t.Fatalf("peak concurrent inspections = %d, limit %d", peak.Load(), sshInventoryConcurrency)
	}
}

func TestSSHInventoryReportsHostsNotStartedBeforeCancellation(t *testing.T) {
	t.Setenv("STEPANEL_SSH_SERVERS", "a,b,c,d,e,f")
	stubSSHInspection(t, func(ctx context.Context, server string) SSHServerStatus {
		<-ctx.Done()
		return SSHServerStatus{Server: server, Error: "cancelled"}
	})
	request := httptest.NewRequest(http.MethodGet, "/api/ssh/inventory", nil)
	ctx, cancel := context.WithCancel(request.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	response := httptest.NewRecorder()
	(&App{}).sshInventory(response, request.WithContext(ctx))
	servers, warnings := decodeSSHInventory(t, response)
	if len(servers) != 6 || len(warnings) != 6 {
		t.Fatalf("servers = %+v, warnings = %#v; want a result and warning per host", servers, warnings)
	}
	for _, server := range servers {
		if server.Server == "" || server.Reachable || server.Error == "" {
			t.Fatalf("server result = %+v, want an explained failure", server)
		}
	}
}

func TestInspectSSHServerBoundsOutput(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nhead -c 1048576 /dev/zero | tr '\\0' x\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status := inspectSSHServer(ctx, "flood")
	if status.Reachable || !strings.Contains(status.Error, "SSH inspection failed") {
		t.Fatalf("status = %+v, want an output-limit failure", status)
	}
	if len(status.Error) > sshInventoryStdoutLimit+sshInventoryStderrLimit+256 {
		t.Fatalf("error carried %d bytes of unbounded output", len(status.Error))
	}
}

// installRecordingSSH puts a fake ssh first on PATH. Like the real client it
// drops options and the host and joins the remote argv with spaces; it writes
// that joined command to the returned file and, when run is true, executes it
// with sh -c as the remote login shell would.
func installRecordingSSH(t *testing.T, run bool) string {
	t.Helper()
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "remote-command")
	script := "#!/bin/sh\nwhile [ \"$1\" = -o ] || [ \"$1\" = -p ]; do shift 2; done\nshift\nprintf '%s' \"$*\" > " + shellQuote(record) + "\n"
	if run {
		script += "exec sh -c \"$*\"\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

// remoteShellWords splits a joined remote command into the argv a POSIX
// remote shell would run, without executing it.
func remoteShellWords(t *testing.T, record string) []string {
	t.Helper()
	joined, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("fake ssh recorded no remote command: %v", err)
	}
	if strings.ContainsAny(string(joined), "$`;&|<>()") {
		t.Fatalf("remote command %q contains shell syntax", joined)
	}
	out, err := exec.Command("sh", "-c", `eval "set -- $1"; printf '%s\n' "$@"`, "sh", string(joined)).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

func TestInspectSSHServerSendsScriptAsOneRemoteWord(t *testing.T) {
	record := installRecordingSSH(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status := inspectSSHServer(ctx, "local")
	if !status.Reachable || status.Hostname == "" || status.OS == "" || status.Disk == "" {
		t.Fatalf("status = %+v, want a complete inventory", status)
	}
	if hostname, err := os.Hostname(); err == nil && status.Hostname != hostname {
		t.Fatalf("hostname = %q, want %q", status.Hostname, hostname)
	}
	joined, err := os.ReadFile(record)
	if err != nil || !strings.HasPrefix(string(joined), "sh -c '") {
		t.Fatalf("remote command = %q, want the script quoted for sh -c", joined)
	}
}

func TestExecuteSSHActionSendsExactRemoteWords(t *testing.T) {
	for _, test := range []struct {
		action, service string
		want            []string
	}{
		{"restart-service", "php8.3-fpm.service", []string{"sudo", "--non-interactive", "systemctl", "restart", "php8.3-fpm.service"}},
		{"restart-service", "getty@tty1.service", []string{"sudo", "--non-interactive", "systemctl", "restart", "getty@tty1.service"}},
		{"reboot", "", []string{"sudo", "--non-interactive", "systemctl", "reboot"}},
	} {
		t.Run(test.action+" "+test.service, func(t *testing.T) {
			record := installRecordingSSH(t, false)
			if err := executeSSHAction(context.Background(), "prod-a", test.action, test.service); err != nil {
				t.Fatal(err)
			}
			if got := remoteShellWords(t, record); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("remote shell words = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExecuteSSHActionRevalidatesStoredRequest(t *testing.T) {
	record := installRecordingSSH(t, false)
	for _, test := range []struct{ server, action, service string }{
		{"prod-a", "restart-service", "nginx; reboot"},
		{"prod-a", "restart-service", "$(id)"},
		{"prod-a", "restart-service", ""},
		{"prod-a", "stop", "nginx"},
		{"-oProxyCommand=id", "reboot", ""},
	} {
		if err := executeSSHAction(context.Background(), test.server, test.action, test.service); err == nil {
			t.Fatalf("executeSSHAction(%q, %q, %q) accepted an invalid request", test.server, test.action, test.service)
		}
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatal("an invalid request reached ssh")
	}
}
