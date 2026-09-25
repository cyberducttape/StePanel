package main

import (
	"bufio"
	"net/http"
	"os"
	"strconv"
	"strings"
)

var siteLogSources = map[string]string{
	"access": "access.log", "error": "error.log", "php-fpm": "php-fpm.log",
	"php": "php.log", "application": "application.log", "deployment": "deployment.log",
	"build": "build.log", "cron": "cron.log", "worker": "worker.log",
}

func (a *App) siteLogs(w http.ResponseWriter, r *http.Request) {
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/logs/"), "/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403); !ok {
		return
	}
	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	filename, ok := siteLogSources[source]
	if !ok {
		http.Error(w, "source must be one of access, error, php-fpm, php, application, deployment, build, cron, worker", 422)
		return
	}
	lines := 200
	if value, err := strconv.Atoi(r.URL.Query().Get("lines")); err == nil && r.URL.Query().Get("lines") != "" {
		lines = value
	}
	if lines < 1 || lines > 1000 {
		http.Error(w, "lines must be between 1 and 1000", 422)
		return
	}
	filter := r.URL.Query().Get("filter")
	path, err := safePath(a.Config.WebRoot, "sites", site, "logs", filename)
	if err != nil {
		http.Error(w, "invalid log path", 422)
		return
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		writeJSON(w, 200, map[string]any{"site": site, "source": source, "lines": []string{}, "available": false})
		return
	}
	if err != nil {
		http.Error(w, "log is unavailable", 503)
		return
	}
	defer file.Close()
	all := make([]string, 0, lines)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if filter != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(filter)) {
			continue
		}
		all = append(all, line)
		if len(all) > lines {
			all = all[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		http.Error(w, "log could not be read", 503)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+site+`-`+source+`.log"`)
		_, _ = w.Write([]byte(strings.Join(all, "\n")))
		return
	}
	writeJSON(w, 200, map[string]any{"site": site, "source": source, "lines": all, "available": true, "filtered": filter != ""})
}
