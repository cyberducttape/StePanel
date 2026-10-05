// Command stepanelctl provides a small operator-facing read-only CLI for the
// same authenticated API used by the web workspace. Mutating subcommands will
// be added only when their confirmation and dry-run contracts are stable.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	base := flag.String("url", envOr("STEPANEL_URL", "http://127.0.0.1:8080"), "StePanel base URL")
	token := flag.String("token", os.Getenv("STEPANEL_TOKEN"), "API token (prefer STEPANEL_TOKEN)")
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	path, ok := commandPath(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "unsupported command; run stepanelctl help")
		os.Exit(2)
	}
	if len(args) == 3 && args[0] == "jobs" && args[1] == "watch" {
		if err := watch(*base, *token, path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := get(*base, *token, path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `stepanelctl — operator CLI

Environment:
  STEPANEL_URL    StePanel base URL (default http://127.0.0.1:8080)
  STEPANEL_TOKEN  scoped administrator API token

Commands:
  status                 show readiness
  doctor                 show system diagnostics
  doctor --production    verify production prerequisites
  capabilities           explain verified host capabilities
  sites list             list sites
  site inspect NAME      inspect one site
  jobs list              list durable jobs
  jobs watch ID          wait for one durable job to finish

Use -url and -token to override the environment.`)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func commandPath(args []string) (string, bool) {
	switch {
	case len(args) == 1 && args[0] == "status":
		return "/readyz", true
	case len(args) == 1 && args[0] == "doctor":
		return "/api/admin/production-readiness", true
	case len(args) == 2 && args[0] == "doctor" && args[1] == "--production":
		return "/api/admin/production-readiness", true
	case len(args) == 1 && args[0] == "capabilities":
		return "/api/capabilities", true
	case len(args) == 2 && args[0] == "sites" && args[1] == "list":
		return "/api/sites/overview", true
	case len(args) == 3 && args[0] == "site" && args[1] == "inspect" && args[2] != "":
		return "/api/sites/overview/" + args[2], true
	case len(args) == 2 && args[0] == "jobs" && args[1] == "list":
		return "/api/jobs", true
	case len(args) == 3 && args[0] == "jobs" && args[1] == "watch" && args[2] != "":
		return "/api/jobs/" + args[2], true
	default:
		return "", false
	}
}

func watch(base, token, path string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	for {
		body, status, err := fetch(client, base, token, path)
		if err != nil {
			return err
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("%s returned HTTP %d: %s", path, status, strings.TrimSpace(string(body)))
		}
		var job struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(body, &job); err != nil {
			return fmt.Errorf("decode job response: %w", err)
		}
		switch job.State {
		case "completed", "failed", "dead-letter", "cancelled":
			_, err := os.Stdout.Write(body)
			if err == nil && (len(body) == 0 || body[len(body)-1] != '\n') {
				_, err = fmt.Fprintln(os.Stdout)
			}
			return err
		}
		time.Sleep(time.Second)
	}
}

func get(base, token, path string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	body, status, err := fetch(client, base, token, path)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s returned HTTP %d: %s", path, status, strings.TrimSpace(string(body)))
	}
	_, err = os.Stdout.Write(body)
	if err == nil && (len(body) == 0 || body[len(body)-1] != '\n') {
		_, err = fmt.Fprintln(os.Stdout)
	}
	return err
}

func fetch(client *http.Client, base, token, path string) ([]byte, int, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, 0, fmt.Errorf("STEPANEL_URL is empty")
	}
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}
	return body, resp.StatusCode, nil
}
