package stepanel

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
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
	if end == 0 || count < 1 || maxBytes < 1 {
		return []string{}, nil
	}
	window := min(end, maxBytes)
	windowStart := end - window
	cursor := end
	// Allocate once and fill the tail window in place. Growing the slice by
	// prepending each earlier chunk recopies all bytes read so far and becomes
	// quadratic for filtered searches near maxFilteredLogBytes.
	data := make([]byte, window)
	position := len(data)
	newlines := int64(0)
	for position > 0 {
		chunk := int64(32 << 10)
		if chunk > int64(position) {
			chunk = int64(position)
		}
		if chunk > cursor-windowStart {
			chunk = cursor - windowStart
		}
		readStart := cursor - chunk
		position -= int(chunk)
		buf := data[position : position+int(chunk)]
		if n, err := file.ReadAt(buf, readStart); err != nil && err != io.EOF {
			return nil, err
		} else if n != len(buf) {
			return nil, io.ErrUnexpectedEOF
		}
		newlines += int64(bytes.Count(buf, []byte{'\n'}))
		cursor = readStart
		if newlines > count && filter == "" {
			break
		}
	}
	data = data[position:]
	if windowStart > 0 {
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
	needle := []byte(strings.ToLower(filter))
	result := make([]string, 0, count)
	for i := len(parts) - 1; i >= 0 && int64(len(result)) < count; i-- {
		lineBytes := bytes.TrimSuffix(parts[i], []byte{'\r'})
		if len(needle) > 0 && !bytes.Contains(bytes.ToLower(lineBytes), needle) {
			continue
		}
		result = append(result, string(lineBytes))
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
	file, err := openManagedSiteLog(a.Config.WebRoot, site, filename)
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

// openManagedSiteLog walks the trusted sites/site/logs hierarchy through
// directory descriptors and opens only a regular, non-symlink log file. The
// descriptor remains confined even if a customer concurrently renames entries.
func openManagedSiteLog(webRoot, site, filename string) (*os.File, error) {
	root, err := os.OpenRoot(webRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	sites, err := openChildRoot(root, "sites")
	if err != nil {
		return nil, err
	}
	defer sites.Close()
	siteRoot, err := openChildRoot(sites, site)
	if err != nil {
		return nil, err
	}
	defer siteRoot.Close()
	logs, err := openChildRoot(siteRoot, "logs")
	if err != nil {
		return nil, err
	}
	defer logs.Close()
	entry, err := logs.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !entry.Mode().IsRegular() {
		return nil, errors.New("log is not a regular file")
	}
	file, err := logs.OpenFile(filename, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(entry, opened) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("log file changed while opening")
	}
	return file, nil
}

func openChildRoot(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("managed log directory is not a real directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = child.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("managed log directory changed while opening")
	}
	return child, nil
}
