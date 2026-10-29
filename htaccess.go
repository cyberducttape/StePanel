package stepanel

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

const maxHTAccessBytes = 256 << 10

var redirectSourcePattern = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*$`)

type htaccessRequest struct {
	Site         string `json:"site"`
	Domain       string `json:"domain"`
	Content      string `json:"content"`
	Action       string `json:"action"`
	AllowPartial bool   `json:"allow_partial"`
	PreviewToken string `json:"preview_token,omitempty"`
}

type htaccessConversion struct {
	CaddyDirectives  string   `json:"caddy_directives"`
	Warnings         []string `json:"warnings"`
	Supported        int      `json:"supported_directives"`
	Applied          bool     `json:"applied"`
	PreviewToken     string   `json:"preview_token,omitempty"`
	ExpiresAt        string   `json:"expires_at,omitempty"`
	Effects          []string `json:"effects,omitempty"`
	RecoveryPlan     []string `json:"recovery_plan,omitempty"`
	RecoverySnapshot string   `json:"recovery_snapshot,omitempty"`
}

func translateHTAccess(content string) (htaccessConversion, error) {
	result := htaccessConversion{Warnings: []string{}}
	if len(content) > maxHTAccessBytes {
		return result, fmt.Errorf(".htaccess content exceeds %d bytes", maxHTAccessBytes)
	}
	var directives []string
	conditionFile, conditionDirectory := false, false
	frontControllerAdded := false
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		fields := strings.Fields(raw)
		lower := strings.ToLower(raw)
		switch {
		case strings.HasPrefix(lower, "<ifmodule ") && strings.HasSuffix(lower, ">"), lower == "</ifmodule>":
			result.Supported++
		case len(fields) == 2 && strings.EqualFold(fields[0], "RewriteEngine") && strings.EqualFold(fields[1], "On"):
			result.Supported++
		case len(fields) == 2 && strings.EqualFold(fields[0], "RewriteBase") && fields[1] == "/":
			result.Supported++
		case len(fields) == 2 && strings.EqualFold(fields[0], "Options") && fields[1] == "-Indexes":
			// Caddy's file_server does not enable directory browsing by default.
			result.Supported++
		case strings.EqualFold(raw, `RewriteRule .* - [E=HTTP_AUTHORIZATION:%{HTTP:Authorization}]`):
			// Caddy forwards Authorization to PHP-FPM without this Apache workaround.
			result.Supported++
		case len(fields) >= 3 && strings.EqualFold(fields[0], "RewriteCond") && strings.EqualFold(fields[1], "%{REQUEST_FILENAME}") && fields[2] == "!-f":
			conditionFile = true
			result.Supported++
		case len(fields) >= 3 && strings.EqualFold(fields[0], "RewriteCond") && strings.EqualFold(fields[1], "%{REQUEST_FILENAME}") && fields[2] == "!-d":
			conditionDirectory = true
			result.Supported++
		case len(fields) >= 3 && strings.EqualFold(fields[0], "RewriteRule") && (fields[1] == `^index\.php$` || fields[1] == `^index.php$`) && fields[2] == "-" && strings.Contains(strings.ToUpper(strings.Join(fields[3:], " ")), "[L"):
			result.Supported++
			conditionFile, conditionDirectory = false, false
		case len(fields) >= 3 && strings.EqualFold(fields[0], "RewriteRule") && isFrontControllerTarget(fields[2]) && conditionFile && conditionDirectory:
			if !frontControllerAdded {
				directives = append(directives, "try_files {path} {path}/ /index.php?{query}")
				frontControllerAdded = true
			}
			result.Supported++
			conditionFile, conditionDirectory = false, false
		case len(fields) == 3 || len(fields) == 4:
			directive, ok := translateRedirect(fields)
			if ok {
				directives = append(directives, directive)
				result.Supported++
				conditionFile, conditionDirectory = false, false
				continue
			}
			fallthrough
		default:
			result.Warnings = append(result.Warnings, fmt.Sprintf("line %d: unsupported directive %q", lineNumber, truncateHTAccessLine(raw)))
			conditionFile, conditionDirectory = false, false
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("scan .htaccess: %w", err)
	}
	result.CaddyDirectives = strings.Join(directives, "\n")
	if result.CaddyDirectives != "" {
		result.CaddyDirectives += "\n"
	}
	return result, nil
}

func isFrontControllerTarget(target string) bool {
	target = strings.SplitN(target, "?", 2)[0]
	return target == "/index.php" || target == "index.php"
}

func translateRedirect(fields []string) (string, bool) {
	if !strings.EqualFold(fields[0], "Redirect") {
		return "", false
	}
	code, source, destination := 302, "", ""
	if len(fields) == 3 {
		source, destination = fields[1], fields[2]
	} else {
		source, destination = fields[2], fields[3]
		switch strings.ToLower(fields[1]) {
		case "permanent":
			code = 301
		case "temp":
			code = 302
		default:
			parsed, err := strconv.Atoi(fields[1])
			if err != nil || parsed < 300 || parsed > 399 {
				return "", false
			}
			code = parsed
		}
	}
	if !redirectSourcePattern.MatchString(source) || !safeRedirectTarget(destination) {
		return "", false
	}
	return fmt.Sprintf("redir %s %s %d", source, destination, code), true
}

func safeRedirectTarget(target string) bool {
	if target == "" || strings.ContainsAny(target, " \t\r\n{}\"") {
		return false
	}
	if strings.HasPrefix(target, "/") {
		if strings.HasPrefix(target, "//") || strings.Contains(target, "#") {
			return false
		}
		return redirectSourcePattern.MatchString(strings.SplitN(target, "?", 2)[0])
	}
	parsed, err := url.Parse(target)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func truncateHTAccessLine(line string) string {
	if len(line) <= 160 {
		return line
	}
	return line[:157] + "..."
}

func (a *App) htaccessMigration(w http.ResponseWriter, r *http.Request) {
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input htaccessRequest
	if err := decodeJSON(w, r, maxHTAccessBytes+8192, &input); err != nil {
		http.Error(w, "invalid JSON or .htaccess content is too large", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(input.Site)
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	if input.Action == "" {
		input.Action = "preview"
	}
	if input.Action != "preview" && input.Action != "apply" {
		http.Error(w, "action must be preview or apply", http.StatusUnprocessableEntity)
		return
	}
	conversion, err := translateHTAccess(input.Content)
	if err != nil {
		writePublicError(w, r, http.StatusUnprocessableEntity, publicError("invalid_htaccess", "the .htaccess content could not be converted", err))
		return
	}
	if a.Config.WebServer != "caddy" {
		http.Error(w, ".htaccess migration can only be applied when Caddy is selected", http.StatusConflict)
		return
	}
	if input.Site == "" || !domainname.Valid(input.Domain) {
		http.Error(w, "a valid site and domain are required", http.StatusUnprocessableEntity)
		return
	}
	if input.Action == "apply" && len(conversion.Warnings) > 0 && !input.AllowPartial {
		writeJSON(w, http.StatusUnprocessableEntity, conversion)
		return
	}
	publicRoot, err := siteDocRoot(a.Config, input.Site)
	if err != nil {
		http.Error(w, "invalid site root", http.StatusUnprocessableEntity)
		return
	}
	if info, err := os.Stat(publicRoot); err != nil || !info.IsDir() {
		http.Error(w, "site document root does not exist", http.StatusUnprocessableEntity)
		return
	}
	if !a.Config.Production && a.Config.VHostCtl == "" {
		http.Error(w, "Caddy site helper is unavailable", http.StatusServiceUnavailable)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLocksContext(r.Context(), input.Site, "vhost:"+siteVHostConfigName(a.Config.WebServer, input.Site, input.Domain))
	if lockErr != nil {
		http.Error(w, "site configuration is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "configuration migration cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	vhostName := siteVHostConfigName(a.Config.WebServer, input.Site, input.Domain)
	currentVhost, err := a.snapshotVhost(vhostName)
	if err != nil {
		http.Error(w, "could not safely inspect current web-server configuration", http.StatusServiceUnavailable)
		return
	}
	if currentVhost.Exists {
		if _, managed := a.routeStateFor(vhostName); !managed {
			http.Error(w, "a vhost file already exists without managed route state; refusing to overwrite it", http.StatusConflict)
			return
		}
	}
	before := struct {
		Vhost   vhostSnapshot `json:"vhost"`
		Content string        `json:"source_sha256"`
	}{Vhost: currentVhost, Content: fmt.Sprintf("%x", sha256.Sum256([]byte(input.Content)))}
	proposed := struct {
		Site         string `json:"site"`
		Domain       string `json:"domain"`
		VHostPath    string `json:"vhost_path"`
		Directives   string `json:"directives"`
		AllowPartial bool   `json:"allow_partial"`
	}{input.Site, input.Domain, filepath.Join(a.Config.VHostRoot, vhostName), conversion.CaddyDirectives, input.AllowPartial}
	if input.Action == "preview" {
		recoveryPlan := []string{"If Caddy rejects the new configuration, StePanel leaves the active configuration unchanged; inspect the Caddy error and retry."}
		if currentVhost.Exists {
			recoveryPlan = append(recoveryPlan, "A mode-0600 copy of the prior vhost will be saved under STEPANEL_RECOVERY_ROOT/webserver-config before mutation; restore that file, validate Caddy, and reload it to roll back.")
		} else {
			recoveryPlan = append(recoveryPlan, "No prior vhost exists; restore the base site configuration with the managed route reconciler to roll back.")
		}
		preview, err := a.makeChangePreview("webserver.htaccess.import:"+vhostName, before, proposed,
			[]string{"Translate and apply the supported Apache directives to the Caddy site configuration.", "Validate and reload Caddy; unsupported lines are skipped only when partial application is explicitly allowed."},
			recoveryPlan)
		if err != nil {
			http.Error(w, "could not create web-server change preview", http.StatusInternalServerError)
			return
		}
		conversion.PreviewToken, conversion.ExpiresAt = preview.PreviewToken, preview.ExpiresAt
		conversion.Effects, conversion.RecoveryPlan = preview.Effects, preview.RecoveryPlan
		writeJSON(w, http.StatusOK, conversion)
		return
	}
	if !a.verifyChangePreview(input.PreviewToken, "webserver.htaccess.import:"+vhostName, before, proposed) {
		http.Error(w, "web-server configuration or source changed after preview; preview again before applying", http.StatusConflict)
		return
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "site.htaccess-migrated", input.Site, fmt.Sprintf("domain=%s supported=%d warnings=%d", input.Domain, conversion.Supported, len(conversion.Warnings)))
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	recoverySnapshot, err := a.preserveVhostForRecovery(vhostName, currentVhost)
	if err != nil {
		intent.Failed("prior vhost recovery snapshot could not be created")
		http.Error(w, "could not preserve the prior vhost for recovery; configuration was not changed", http.StatusServiceUnavailable)
		return
	}
	conversion.RecoverySnapshot = recoverySnapshot
	ctx, cancel := context.WithTimeout(operationCtx, time.Minute)
	defer cancel()
	var applyErr error
	if a.Config.Production {
		applyErr = runTypedVhostOperationInput(ctx, a.Config, rootbroker.VhostRequest{
			Action:     "import-htaccess",
			Site:       input.Site,
			Domain:     input.Domain,
			WebServer:  a.Config.WebServer,
			Directives: conversion.CaddyDirectives,
		})
	} else {
		_, applyErr, _ = runAllowlistedHelperOutput(ctx, a.Config, []byte(conversion.CaddyDirectives), a.Config.VHostCtl, "import-htaccess", input.Site, input.Domain)
	}
	if applyErr != nil {
		intent.Failed("Caddy rejected the translated configuration")
		http.Error(w, "Caddy rejected the translated configuration", http.StatusServiceUnavailable)
		return
	}
	intent.Completed("configuration applied config=" + vhostName + " recovery_snapshot=" + recoverySnapshot)
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "configuration migration cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	conversion.Applied = true
	writeJSON(w, http.StatusAccepted, conversion)
}
