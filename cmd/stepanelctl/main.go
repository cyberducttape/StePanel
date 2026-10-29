// Command stepanelctl automates the same documented durable API workflows as
// the StePanel dashboard.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/sdk/go/stepanel"
)

func main() {
	base := flag.String("url", envOr("STEPANEL_URL", "http://127.0.0.1:8080"), "StePanel base URL")
	token := flag.String("token", os.Getenv("STEPANEL_TOKEN"), "scoped API token (prefer STEPANEL_TOKEN)")
	flag.Usage = usage
	flag.Parse()
	if err := run(*base, *token, flag.Args(), os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `stepanelctl — API-contract based automation CLI

Environment: STEPANEL_URL (default http://127.0.0.1:8080), STEPANEL_TOKEN

Commands:
  status | doctor [--production] | capabilities
  sites list | site inspect NAME | site create --site NAME [--template php|wordpress]
  backup list [--site NAME] | backup create --site NAME [--include-databases] [--wait]
  restore files|database|staging ... (live restores require exact confirmation)
  migration inspect --file ARCHIVE | migration import --upload-id ID --username NAME --confirm IMPORT
  diagnostics collect --output FILE
  provision apply --file PLAN.json [--wait]
  jobs list | jobs watch ID
  completion bash|zsh|fish

All mutations call the same authenticated API operations as the dashboard.
Use a scoped bearer token; the server enforces authorization and admission.`)
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// commandPath remains useful to callers building simple read-only automation
// without invoking the command dispatcher.
func commandPath(args []string) (string, bool) {
	switch {
	case len(args) == 1 && args[0] == "status":
		return "/readyz", true
	case len(args) == 1 && args[0] == "doctor":
		return "/api/v1/admin/production-readiness", true
	case len(args) == 2 && args[0] == "doctor" && args[1] == "--production":
		return "/api/v1/admin/production-readiness", true
	case len(args) == 1 && args[0] == "capabilities":
		return "/api/v1/capabilities", true
	case len(args) == 2 && args[0] == "sites" && args[1] == "list":
		return "/api/v1/sites/overview", true
	case len(args) == 3 && args[0] == "site" && args[1] == "inspect" && args[2] != "":
		return "/api/v1/sites/overview/" + urlPath(args[2]), true
	case len(args) == 2 && args[0] == "jobs" && args[1] == "list":
		return "/api/v1/jobs", true
	case len(args) == 3 && args[0] == "jobs" && args[1] == "watch" && args[2] != "":
		return "/api/v1/jobs/" + urlPath(args[2]), true
	default:
		return "", false
	}
}

func run(base, token string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		usage()
		return nil
	}
	if args[0] == "completion" {
		if len(args) != 2 {
			return errors.New("usage: stepanelctl completion bash|zsh|fish")
		}
		return writeCompletion(args[1], stdout)
	}
	c := stepanel.New(base, token)
	ctx := context.Background()
	switch args[0] {
	case "status":
		return get(c, ctx, "/readyz", stdout)
	case "capabilities":
		return getOperation(c, ctx, "getCapabilities", stdout)
	case "doctor":
		return getOperation(c, ctx, "checkProductionReadiness", stdout)
	case "sites":
		if len(args) == 2 && args[1] == "list" {
			return getOperation(c, ctx, "listSites", stdout)
		}
	case "site":
		if len(args) >= 3 && args[1] == "inspect" {
			_, p, _ := stepanel.OperationPath("inspectSite")
			return get(c, ctx, p+urlPath(args[2]), stdout)
		}
		if len(args) >= 2 && args[1] == "create" {
			return createSite(c, ctx, args[2:], stdin, stdout)
		}
	case "backup":
		if len(args) >= 2 && args[1] == "list" {
			fs := flag.NewFlagSet("backup list", flag.ContinueOnError)
			fs.SetOutput(stderr)
			site := fs.String("site", "", "site filter")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			_, p, _ := stepanel.OperationPath("listBackups")
			if *site != "" {
				p += "?site=" + urlQuery(*site)
			}
			return get(c, ctx, p, stdout)
		}
		if len(args) >= 2 && args[1] == "create" {
			return createBackup(c, ctx, args[2:], stdout)
		}
	case "restore":
		if len(args) >= 2 && args[1] == "staging" {
			return restoreStaging(c, ctx, args[2:], stdin, stdout)
		}
		if len(args) >= 2 && (args[1] == "files" || args[1] == "database") {
			return restoreLive(c, ctx, args[1], args[2:], stdout)
		}
	case "migration":
		if len(args) >= 2 && args[1] == "inspect" {
			return migrationInspect(c, ctx, args[2:], stdout)
		}
		if len(args) >= 2 && args[1] == "import" {
			return migrationImport(c, ctx, args[2:], stdout)
		}
	case "diagnostics":
		if len(args) >= 2 && args[1] == "collect" {
			return collectDiagnostics(c, ctx, args[2:], stdout)
		}
	case "provision":
		if len(args) >= 2 && args[1] == "apply" {
			return provision(c, ctx, args[2:], stdout)
		}
	case "jobs":
		if len(args) == 2 && args[1] == "list" {
			return getOperation(c, ctx, "listJobs", stdout)
		}
		if len(args) == 3 && args[1] == "watch" {
			return watch(c, ctx, args[2], stdout)
		}
	}
	return fmt.Errorf("unsupported command; run stepanelctl help")
}

func createSite(c *stepanel.Client, ctx context.Context, args []string, stdin io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("site create", flag.ContinueOnError)
	site := fs.String("site", "", "site name")
	template := fs.String("template", "php", "php or wordpress")
	domain := fs.String("domain", "", "WordPress domain")
	title := fs.String("title", "", "WordPress title")
	user := fs.String("admin-user", "", "WordPress administrator")
	email := fs.String("admin-email", "", "WordPress administrator email")
	passwordStdin := fs.Bool("admin-password-stdin", false, "read WordPress admin password from stdin")
	key := fs.String("idempotency-key", "", "stable retry key")
	wait := fs.Bool("wait", false, "wait for durable job completion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *site == "" {
		return errors.New("site create requires --site")
	}
	payload := map[string]any{"site": *site, "template": *template}
	if *template == "wordpress" {
		if !*passwordStdin {
			return errors.New("WordPress provisioning requires --admin-password-stdin")
		}
		p, err := io.ReadAll(io.LimitReader(stdin, 130))
		if err != nil {
			return err
		}
		pass := strings.TrimRight(string(p), "\r\n")
		if len(pass) < 20 || len(pass) > 128 {
			return errors.New("password from stdin must be 20–128 bytes")
		}
		payload["domain"], payload["title"], payload["admin_user"], payload["admin_email"], payload["admin_password"] = *domain, *title, *user, *email, pass
	}
	resp, err := c.JSONOperation(ctx, "createSite", payload, keyHeader(*key))
	if err != nil {
		return err
	}
	if *wait {
		return emitAndWatch(c, ctx, resp, out)
	}
	return emit(resp, out)
}

func createBackup(c *stepanel.Client, ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("backup create", flag.ContinueOnError)
	site := fs.String("site", "", "site name")
	db := fs.Bool("include-databases", false, "include managed database dumps")
	wait := fs.Bool("wait", false, "wait for durable job completion")
	key := fs.String("idempotency-key", "", "stable retry key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *site == "" {
		return errors.New("backup create requires --site")
	}
	resp, err := c.JSONOperation(ctx, "createBackup", map[string]any{"site": *site, "include_databases": *db}, keyHeader(*key))
	if err != nil {
		return err
	}
	if *wait {
		return emitAndWatch(c, ctx, resp, out)
	}
	return emit(resp, out)
}

func restoreStaging(c *stepanel.Client, ctx context.Context, args []string, stdin io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("restore staging", flag.ContinueOnError)
	backup := fs.String("backup", "", "verified backup directory/name")
	site := fs.String("site", "", "new staging site name")
	domain := fs.String("domain", "", "staging domain")
	source := fs.String("source-site", "", "source site")
	db := fs.String("database", "", "dump name to restore")
	targetDB := fs.String("target-database", "", "new managed staging database")
	targetUser := fs.String("target-user", "", "new database user")
	passwordStdin := fs.Bool("target-password-stdin", false, "read target database password from stdin")
	wait := fs.Bool("wait", false, "wait for durable job completion")
	key := fs.String("idempotency-key", "", "stable retry key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *backup == "" || *site == "" || *domain == "" {
		return errors.New("restore staging requires --backup, --site, and --domain")
	}
	payload := map[string]any{"backup": *backup, "site": *site, "domain": *domain}
	if *source != "" {
		payload["source_site"] = *source
	}
	if *db != "" {
		payload["database"] = *db
	}
	if *targetDB != "" {
		payload["target_database"] = *targetDB
	}
	if *targetUser != "" {
		payload["target_user"] = *targetUser
	}
	if *passwordStdin {
		p, e := io.ReadAll(io.LimitReader(stdin, 4097))
		if e != nil {
			return e
		}
		payload["target_password"] = strings.TrimRight(string(p), "\r\n")
	}
	resp, err := c.JSONOperation(ctx, "restoreBackupToStaging", payload, keyHeader(*key))
	if err != nil {
		return err
	}
	if *wait {
		return emitAndWatch(c, ctx, resp, out)
	}
	return emit(resp, out)
}

func restoreLive(c *stepanel.Client, ctx context.Context, mode string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("restore "+mode, flag.ContinueOnError)
	backup := fs.String("backup", "", "verified backup name")
	site := fs.String("site", "", "target site")
	database := fs.String("database", "", "managed database to restore (database mode)")
	confirm := fs.String("confirm", "", "exact confirmation: RESTORE_FILES or RESTORE_DATABASE")
	key := fs.String("idempotency-key", "", "stable retry key")
	wait := fs.Bool("wait", false, "wait for durable job completion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	expected := "RESTORE_FILES"
	op := "restoreBackupFiles"
	body := map[string]any{"backup": *backup, "site": *site}
	if mode == "database" {
		expected = "RESTORE_DATABASE"
		op = "restoreBackupDatabase"
		if *database == "" {
			return errors.New("restore database requires --database")
		}
		body["database"] = *database
	}
	if *backup == "" || *site == "" || *confirm != expected {
		return fmt.Errorf("restore %s requires --backup, --site, and --confirm %s", mode, expected)
	}
	body["confirm"] = *confirm
	resp, err := c.JSONOperation(ctx, op, body, keyHeader(*key))
	if err != nil {
		return err
	}
	if *wait {
		return emitAndWatch(c, ctx, resp, out)
	}
	return emit(resp, out)
}

func migrationInspect(c *stepanel.Client, ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("migration inspect", flag.ContinueOnError)
	file := fs.String("file", "", "cPanel archive file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("migration inspect requires --file")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	resp, err := c.UploadOperation(ctx, "inspectMigrationArchive", "backup", filepath.Base(*file), f, info.Size(), nil)
	if err != nil {
		return err
	}
	return emit(resp, out)
}

func migrationImport(c *stepanel.Client, ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("migration import", flag.ContinueOnError)
	upload := fs.String("upload-id", "", "ID returned by migration inspect")
	username := fs.String("username", "", "target account")
	confirm := fs.String("confirm", "", "must be IMPORT")
	db := fs.Bool("restore-databases", false, "restore included databases")
	key := fs.String("idempotency-key", "", "stable retry key")
	wait := fs.Bool("wait", false, "wait for durable job completion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *upload == "" || *username == "" || *confirm != "IMPORT" {
		return errors.New("migration import requires --upload-id, --username, and --confirm IMPORT")
	}
	fields := map[string]string{"upload_id": *upload, "username": *username, "confirm": *confirm}
	if *db {
		fields["restore_databases"] = "on"
	}
	headers := keyHeader(*key)
	resp, err := c.MultipartOperation(ctx, "importMigrationArchive", fields, headers)
	if err != nil {
		return err
	}
	if *wait {
		return emitAndWatch(c, ctx, resp, out)
	}
	return emit(resp, out)
}

func collectDiagnostics(c *stepanel.Client, ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("diagnostics collect", flag.ContinueOnError)
	dest := fs.String("output", "stepanel-support.zip", "destination ZIP")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resp, err := c.DoOperation(ctx, "collectSupportBundle", nil, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.Status < 200 || resp.Status >= 300 {
		return responseError(resp)
	}
	f, err := os.OpenFile(*dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	copied, copyErr := io.Copy(f, io.LimitReader(resp.Body, (512<<20)+1))
	if copyErr == nil && copied > 512<<20 {
		copyErr = errors.New("support bundle exceeds 512 MiB output limit")
	}
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(*dest)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(*dest)
		return closeErr
	}
	_, err = fmt.Fprintf(out, "redacted diagnostics saved to %s (request %s)\n", *dest, resp.RequestID)
	return err
}

type provisionPlan struct {
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Sites          []struct {
		Site          string `json:"site"`
		Template      string `json:"template"`
		Domain        string `json:"domain,omitempty"`
		Title         string `json:"title,omitempty"`
		AdminUser     string `json:"admin_user,omitempty"`
		AdminEmail    string `json:"admin_email,omitempty"`
		AdminPassword string `json:"admin_password,omitempty"`
	} `json:"sites"`
}

func provision(c *stepanel.Client, ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("provision apply", flag.ContinueOnError)
	file := fs.String("file", "", "JSON provisioning plan")
	wait := fs.Bool("wait", false, "wait for each site job")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("provision apply requires --file")
	}
	info, err := os.Stat(*file)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("provision plan contains secrets; restrict its permissions to 0600")
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var plan provisionPlan
	if err = json.Unmarshal(data, &plan); err != nil {
		return err
	}
	if len(plan.Sites) == 0 {
		return errors.New("provision plan has no sites")
	}
	for _, s := range plan.Sites {
		if strings.TrimSpace(s.Site) == "" {
			return errors.New("every provisioned site requires site")
		}
		payload := map[string]any{"site": s.Site, "template": s.Template}
		if s.Template == "wordpress" {
			payload["domain"], payload["title"], payload["admin_user"], payload["admin_email"], payload["admin_password"] = s.Domain, s.Title, s.AdminUser, s.AdminEmail, s.AdminPassword
		}
		resp, err := c.JSONOperation(ctx, "createSite", payload, keyHeader(plan.IdempotencyKey))
		if err != nil {
			return fmt.Errorf("provision %s: %w", s.Site, err)
		}
		if *wait {
			if err = emitAndWatch(c, ctx, resp, out); err != nil {
				return err
			}
		} else if err = emit(resp, out); err != nil {
			return err
		}
	}
	return nil
}

func get(c *stepanel.Client, ctx context.Context, p string, out io.Writer) error {
	r, e := c.Do(ctx, http.MethodGet, p, nil, "", nil)
	if e != nil {
		return e
	}
	return emit(r, out)
}

func getOperation(c *stepanel.Client, ctx context.Context, operationID string, out io.Writer) error {
	r, err := c.DoOperation(ctx, operationID, nil, "", nil)
	if err != nil {
		return err
	}
	return emit(r, out)
}
func jsonCall(c *stepanel.Client, ctx context.Context, m, p string, v any, h map[string]string) (*stepanel.Response, error) {
	return c.JSON(ctx, m, p, v, h)
}
func emit(r *stepanel.Response, out io.Writer) error {
	defer r.Body.Close()
	if r.Status < 200 || r.Status >= 300 {
		return responseError(r)
	}
	_, e := io.Copy(out, io.LimitReader(r.Body, 8<<20))
	if e == nil {
		_, e = io.WriteString(out, "\n")
	}
	return e
}
func responseError(r *stepanel.Response) error {
	defer r.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return fmt.Errorf("StePanel returned HTTP %d (request %s): %s", r.Status, r.RequestID, strings.TrimSpace(string(body)))
}
func watch(c *stepanel.Client, ctx context.Context, id string, out io.Writer) error {
	return watchPath(c, ctx, "/api/v1/jobs/"+urlPath(id), out)
}
func emitAndWatch(c *stepanel.Client, ctx context.Context, r *stepanel.Response, out io.Writer) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if r.Status < 200 || r.Status >= 300 {
		return fmt.Errorf("StePanel returned HTTP %d (request %s): %s", r.Status, r.RequestID, strings.TrimSpace(string(body)))
	}
	if _, err = out.Write(append(body, '\n')); err != nil {
		return err
	}
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err = json.Unmarshal(body, &accepted); err != nil {
		return err
	}
	if accepted.JobID == "" {
		return errors.New("response did not include a durable job_id")
	}
	return watch(c, ctx, accepted.JobID, out)
}
func watchPath(c *stepanel.Client, ctx context.Context, p string, out io.Writer) error {
	deadline := time.NewTimer(24 * time.Hour)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		r, err := c.Do(ctx, http.MethodGet, p, nil, "", nil)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		r.Body.Close()
		if readErr != nil {
			return readErr
		}
		if r.Status < 200 || r.Status >= 300 {
			return fmt.Errorf("job status HTTP %d (request %s): %s", r.Status, r.RequestID, strings.TrimSpace(string(body)))
		}
		var job struct {
			State string `json:"state"`
		}
		if err = json.Unmarshal(body, &job); err != nil {
			return err
		}
		switch job.State {
		case "completed", "failed", "dead-letter", "cancelled":
			_, err = out.Write(append(body, '\n'))
			if job.State != "completed" && err == nil {
				return fmt.Errorf("job ended in state %s", job.State)
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("timed out waiting for job; it remains available through jobs watch")
		case <-ticker.C:
		}
	}
}
func keyHeader(key string) map[string]string {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	return map[string]string{"Idempotency-Key": strings.TrimSpace(key)}
}
func urlPath(v string) string {
	if v == "" || strings.ContainsAny(v, "/\\?#") {
		return "invalid"
	}
	return v
}
func urlQuery(v string) string { return url.QueryEscape(v) }

func writeCompletion(shell string, out io.Writer) error {
	content, ok := generatedCompletions[shell]
	if !ok {
		return errors.New("completion shell must be bash, zsh, or fish")
	}
	_, err := io.WriteString(out, content)
	return err
}
