package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxHTAccessBytes = 256 << 10

var redirectSourcePattern = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*$`)

type htaccessRequest struct {
	Site         string `json:"site"`
	Domain       string `json:"domain"`
	Content      string `json:"content"`
	Action       string `json:"action"`
	AllowPartial bool   `json:"allow_partial"`
}

type htaccessConversion struct {
	CaddyDirectives string   `json:"caddy_directives"`
	Warnings        []string `json:"warnings"`
	Supported       int      `json:"supported_directives"`
	Applied         bool     `json:"applied"`
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
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if input.Action == "preview" {
		writeJSON(w, http.StatusOK, conversion)
		return
	}
	if a.Config.WebServer != "caddy" {
		http.Error(w, ".htaccess migration can only be applied when Caddy is selected", http.StatusConflict)
		return
	}
	if input.Site == "" || !domainPattern.MatchString(input.Domain) {
		http.Error(w, "a valid site and domain are required", http.StatusUnprocessableEntity)
		return
	}
	if len(conversion.Warnings) > 0 && !input.AllowPartial {
		writeJSON(w, http.StatusUnprocessableEntity, conversion)
		return
	}
	publicRoot := filepath.Join(a.Config.WebRoot, "sites", input.Site, "public")
	if err := ensureInside(a.Config.WebRoot, publicRoot); err != nil {
		http.Error(w, "invalid site root", http.StatusUnprocessableEntity)
		return
	}
	if info, err := os.Stat(publicRoot); err != nil || !info.IsDir() {
		http.Error(w, "site document root does not exist", http.StatusUnprocessableEntity)
		return
	}
	if a.Config.VHostCtl == "" {
		http.Error(w, "Caddy site helper is unavailable", http.StatusServiceUnavailable)
		return
	}
	releaseUnlock, lockErr := a.acquireSiteMutationLocks(r.Context(), input.Site, "vhost:"+siteVHostConfigName(a.Config.WebServer, input.Site, input.Domain))
	if lockErr != nil {
		http.Error(w, "site configuration is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	command := helperCommandContext(ctx, a.Config, a.Config.VHostCtl, "import-htaccess", input.Site, input.Domain)
	if _, err := runBoundedCommandInput(ctx, command, strings.NewReader(conversion.CaddyDirectives)); err != nil {
		http.Error(w, "Caddy rejected the translated configuration", http.StatusServiceUnavailable)
		return
	}
	conversion.Applied = true
	if err := AuditAs(a.Config.AuditLog, a.Auth.Username, "site.htaccess-migrated", input.Site, fmt.Sprintf("domain=%s supported=%d warnings=%d", input.Domain, conversion.Supported, len(conversion.Warnings))); err != nil {
		http.Error(w, "configuration applied but audit persistence is unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusAccepted, conversion)
}
