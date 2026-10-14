package stepanel

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const maxLogTailBytes = 4 << 20
const maxFilteredLogBytes = 16 << 20

// readLogTail reads backward from EOF, bounding both disk I/O and memory. If
// the bound cuts through a line, that partial leading line is discarded.
func readLogTail(file *os.File, count, maxBytes int64, filter string) ([]string, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	end := info.Size()
	start := end
	var data []byte
	newlines := int64(0)
	for start > 0 && end-start < maxBytes {
		chunk := int64(32 << 10)
		if chunk > maxBytes-(end-start) {
			chunk = maxBytes - (end - start)
		}
		if chunk > start {
			chunk = start
		}
		start -= chunk
		buf := make([]byte, chunk)
		if _, err := file.ReadAt(buf, start); err != nil && err != io.EOF {
			return nil, err
		}
		newlines += int64(bytes.Count(buf, []byte{'\n'}))
		data = append(buf, data...)
		if newlines > count && filter == "" {
			break
		}
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			data = nil
		}
	}
	parts := bytes.Split(data, []byte{'\n'})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	needle := strings.ToLower(filter)
	result := make([]string, 0, count)
	for i := len(parts) - 1; i >= 0 && int64(len(result)) < count; i-- {
		line := string(bytes.TrimSuffix(parts[i], []byte{'\r'}))
		if needle != "" && !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		result = append(result, line)
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result, nil
}

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
	if !a.requireCustomerScope(w, r, "logs:read") {
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
	limit := int64(maxLogTailBytes)
	if filter != "" {
		limit = maxFilteredLogBytes
	}
	all, err := readLogTail(file, int64(lines), limit, filter)
	if err != nil {
		http.Error(w, "log could not be read", 503)
		return
	}
	if filter != "" {
		if info, statErr := file.Stat(); statErr == nil && info.Size() > limit {
			w.Header().Set("X-Log-Search-Truncated", "true")
		}
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+site+`-`+source+`.log"`)
		_, _ = w.Write([]byte(strings.Join(all, "\n")))
		return
	}
	writeJSON(w, 200, map[string]any{"site": site, "source": source, "lines": all, "available": true, "filtered": filter != ""})
}
