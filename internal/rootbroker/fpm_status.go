package rootbroker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	fpmStatusPath        = "/stepanel-fpm-status"
	fpmStatusMaxResponse = 64 << 10
	fpmPoolConfigLimit   = 64 << 10
	fpmStatusTimeout     = 1500 * time.Millisecond
)

var fpmSitePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// FPMStatus is a read-only, bounded snapshot of StePanel-managed PHP-FPM
// pools. It intentionally contains aggregate counters only, never request,
// script, or customer data.
type FPMStatus struct {
	Available   bool            `json:"available"`
	CollectedAt time.Time       `json:"collected_at"`
	Pools       []FPMStatusPool `json:"pools"`
	Warnings    []string        `json:"warnings"`
	Detail      string          `json:"detail,omitempty"`
}

type FPMStatusPool struct {
	PHPVersion         string `json:"php_version,omitempty"`
	State              string `json:"state"`
	ActiveProcesses    uint64 `json:"active_processes"`
	IdleProcesses      uint64 `json:"idle_processes"`
	TotalProcesses     uint64 `json:"total_processes"`
	MaxChildren        uint64 `json:"max_children"`
	ListenQueue        uint64 `json:"listen_queue"`
	MaxListenQueue     uint64 `json:"max_listen_queue"`
	MaxChildrenReached uint64 `json:"max_children_reached"`
	SlowRequests       uint64 `json:"slow_requests"`
	AtCapacity         bool   `json:"at_capacity"`
	Saturated          bool   `json:"saturated"`
}

type fpmPoolConfig struct {
	version string
	socket  string
	max     uint64
}

// collectFPMStatus discovers only root-owned StePanel pool files and only
// connects to sockets directly below the root-owned configured socket root.
// The request itself is FastCGI over the private Unix socket; no HTTP status
// route is added to a vhost.
func collectFPMStatus(ctx context.Context, site, siteUser string) (FPMStatus, error) {
	if !fpmSitePattern.MatchString(site) || siteUser == "" {
		return FPMStatus{}, errors.New("invalid site identity for PHP-FPM status")
	}
	return collectFPMStatusFrom(ctx, site, siteUser, "/etc/ste-panel.env", []string{
		"/etc/php/*/fpm/pool.d",
		"/etc/php-fpm.d",
	})
}

func collectFPMStatusFrom(ctx context.Context, site, siteUser, identityConfig string, poolRoots []string) (FPMStatus, error) {
	result := FPMStatus{CollectedAt: time.Now().UTC(), Pools: []FPMStatusPool{}, Warnings: []string{}}
	socketRoot, err := readFPMSocketRoot(identityConfig)
	if err != nil {
		return result, err
	}
	uid, err := siteUID(siteUser)
	if err != nil {
		return result, err
	}
	pools, err := findManagedFPMPools(site, socketRoot, poolRoots)
	if err != nil {
		return result, err
	}
	if len(pools) == 0 {
		result.Detail = "No managed PHP-FPM pool is configured for this site."
		return result, nil
	}
	for _, pool := range pools {
		item := FPMStatusPool{PHPVersion: pool.version, MaxChildren: pool.max, State: "unavailable"}
		if err := verifyFPMSocket(pool.socket, socketRoot, uid); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("PHP %s pool socket is unavailable or unsafe: %v", pool.version, err))
			result.Pools = append(result.Pools, item)
			continue
		}
		stats, err := requestFPMStatus(ctx, pool.socket)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("PHP %s pool status could not be read: %v", pool.version, err))
			result.Pools = append(result.Pools, item)
			continue
		}
		item.State = "available"
		item.ActiveProcesses = stats.Active
		item.IdleProcesses = stats.Idle
		item.TotalProcesses = stats.Total
		item.ListenQueue = stats.Queue
		item.MaxListenQueue = stats.MaxQueue
		item.MaxChildrenReached = stats.MaxChildrenReached
		item.SlowRequests = stats.SlowRequests
		item.AtCapacity, item.Saturated = fpmPoolIndicators(stats.Active, stats.Queue, pool.max)
		result.Available = true
		result.Pools = append(result.Pools, item)
	}
	if !result.Available {
		result.Detail = "PHP-FPM pools are configured, but no live pool status could be collected."
	} else if len(result.Warnings) > 0 {
		result.Detail = "PHP-FPM status is partial; one or more configured pools could not be sampled."
	}
	return result, nil
}

func fpmPoolIndicators(active, queue, maxChildren uint64) (atCapacity, saturated bool) {
	return maxChildren > 0 && active >= maxChildren, queue > 0
}

func readFPMSocketRoot(path string) (string, error) {
	data, err := readRootOwnedConfig(path, 64<<10)
	if err != nil {
		return "", fmt.Errorf("read authoritative PHP socket configuration: %w", err)
	}
	var socketRoot string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "STEPANEL_PHP_SOCKET_ROOT=") {
			continue
		}
		if socketRoot != "" {
			return "", errors.New("PHP socket root is configured more than once")
		}
		value := strings.TrimPrefix(line, "STEPANEL_PHP_SOCKET_ROOT=")
		if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
			return "", errors.New("PHP socket root configuration is malformed")
		}
		socketRoot = value[1 : len(value)-1]
	}
	if !filepath.IsAbs(socketRoot) || filepath.Clean(socketRoot) != socketRoot || strings.ContainsAny(socketRoot, "\x00\r\n\t ") {
		return "", errors.New("PHP socket root configuration is invalid")
	}
	info, err := os.Lstat(socketRoot)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByRoot(info) || info.Mode().Perm()&0022 != 0 {
		return "", errors.New("PHP socket root must be a root-owned, non-writable real directory")
	}
	return socketRoot, nil
}

func readRootOwnedConfig(path string, limit int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !ownedByRoot(info) || info.Mode().Perm()&0022 != 0 || info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("configuration must be a bounded, root-owned regular file not writable by group or other")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("configuration exceeds its size limit")
	}
	return data, nil
}

func ownedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

func siteUID(name string) (uint32, error) {
	account, err := user.Lookup(name)
	if err != nil {
		return 0, fmt.Errorf("resolve PHP-FPM site account: %w", err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return 0, errors.New("site account has an invalid numeric UID")
	}
	return uint32(uid), nil
}

func findManagedFPMPools(site, socketRoot string, roots []string) ([]fpmPoolConfig, error) {
	filename := "stepanel-" + site + ".conf"
	pools := make([]fpmPoolConfig, 0, 4)
	seen := map[string]bool{}
	for _, pattern := range roots {
		dirs, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("expand PHP-FPM pool path: %w", err)
		}
		for _, dir := range dirs {
			path := filepath.Join(dir, filename)
			data, err := readRootOwnedConfig(path, fpmPoolConfigLimit)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("read managed PHP-FPM pool %s: %w", filename, err)
			}
			version := "default"
			if filepath.Base(dir) == "pool.d" {
				version = filepath.Base(filepath.Dir(filepath.Dir(dir)))
			}
			pool, err := parseManagedFPMPool(data, site, socketRoot, version)
			if err != nil {
				return nil, fmt.Errorf("validate managed PHP-FPM pool %s: %w", filename, err)
			}
			if !seen[pool.socket] {
				seen[pool.socket] = true
				pools = append(pools, pool)
			}
		}
	}
	return pools, nil
}

func parseManagedFPMPool(data []byte, site, socketRoot, version string) (fpmPoolConfig, error) {
	wantedSection := "stepanel-" + site
	section := ""
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 16<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), ";", 2)[0])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section != wantedSection {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fpmPoolConfig{}, errors.New("malformed pool setting")
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "listen", "pm.status_path", "pm.max_children":
			if _, exists := values[key]; exists {
				return fpmPoolConfig{}, fmt.Errorf("duplicate %s setting", key)
			}
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return fpmPoolConfig{}, err
	}
	if values["pm.status_path"] != fpmStatusPath {
		return fpmPoolConfig{}, errors.New("private PHP-FPM status path is not enabled")
	}
	listen := values["listen"]
	if !filepath.IsAbs(listen) || filepath.Dir(listen) != socketRoot || filepath.Clean(listen) != listen || strings.ContainsAny(listen, "\x00\r\n\t ") {
		return fpmPoolConfig{}, errors.New("pool socket is outside the configured socket root")
	}
	base := filepath.Base(listen)
	if !strings.HasPrefix(base, "stepanel-"+site) || !strings.HasSuffix(base, ".sock") || strings.ContainsAny(base, "/\\") {
		return fpmPoolConfig{}, errors.New("pool socket name does not match the site")
	}
	max, err := strconv.ParseUint(values["pm.max_children"], 10, 16)
	if err != nil || max < 1 || max > 512 {
		return fpmPoolConfig{}, errors.New("pool max_children is invalid")
	}
	return fpmPoolConfig{version: version, socket: listen, max: max}, nil
}

func verifyFPMSocket(path, root string, uid uint32) error {
	if filepath.Dir(path) != root {
		return errors.New("socket escaped configured root")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("configured pool path is not a Unix socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid {
		return errors.New("configured pool socket is not owned by the site account")
	}
	return nil
}

type fpmRawStatus struct {
	Active             json.RawMessage `json:"active processes"`
	Idle               json.RawMessage `json:"idle processes"`
	Total              json.RawMessage `json:"total processes"`
	Queue              json.RawMessage `json:"listen queue"`
	MaxQueue           json.RawMessage `json:"max listen queue"`
	MaxChildrenReached json.RawMessage `json:"max children reached"`
	SlowRequests       json.RawMessage `json:"slow requests"`
}

type fpmNumericStatus struct {
	Active, Idle, Total, Queue, MaxQueue, MaxChildrenReached, SlowRequests uint64
}

func requestFPMStatus(ctx context.Context, socket string) (fpmNumericStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, fpmStatusTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return fpmNumericStatus{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := sendFCGIRequest(conn); err != nil {
		return fpmNumericStatus{}, err
	}
	body, err := readFCGIResponse(conn, fpmStatusMaxResponse)
	if err != nil {
		return fpmNumericStatus{}, err
	}
	statusBody, err := fastCGIBody(body)
	if err != nil {
		return fpmNumericStatus{}, err
	}
	var raw fpmRawStatus
	if err := json.Unmarshal(statusBody, &raw); err != nil {
		return fpmNumericStatus{}, fmt.Errorf("decode PHP-FPM JSON status: %w", err)
	}
	values := []*json.RawMessage{&raw.Active, &raw.Idle, &raw.Total, &raw.Queue, &raw.MaxQueue, &raw.MaxChildrenReached, &raw.SlowRequests}
	parsed := make([]uint64, len(values))
	for i, value := range values {
		n, err := parseFPMUint(*value)
		if err != nil {
			return fpmNumericStatus{}, err
		}
		parsed[i] = n
	}
	return fpmNumericStatus{Active: parsed[0], Idle: parsed[1], Total: parsed[2], Queue: parsed[3], MaxQueue: parsed[4], MaxChildrenReached: parsed[5], SlowRequests: parsed[6]}, nil
}

func sendFCGIRequest(w io.Writer) error {
	if err := writeFCGIRecord(w, 1, 1, []byte{0, 1, 0, 0, 0, 0, 0, 0}); err != nil { // BEGIN_REQUEST / RESPONDER
		return err
	}
	params := [][2]string{
		{"GATEWAY_INTERFACE", "CGI/1.1"},
		{"REQUEST_METHOD", "GET"},
		{"REQUEST_URI", fpmStatusPath + "?json"},
		{"SCRIPT_NAME", fpmStatusPath},
		{"SCRIPT_FILENAME", fpmStatusPath},
		{"QUERY_STRING", "json"},
		{"SERVER_PROTOCOL", "HTTP/1.1"},
		{"SERVER_SOFTWARE", "StePanel"},
	}
	encoded := make([]byte, 0, 256)
	for _, pair := range params {
		encoded = appendFCGIParam(encoded, pair[0], pair[1])
	}
	if err := writeFCGIRecord(w, 1, 4, encoded); err != nil { // PARAMS
		return err
	}
	if err := writeFCGIRecord(w, 1, 4, nil); err != nil {
		return err
	}
	return writeFCGIRecord(w, 1, 5, nil) // STDIN terminator
}

func appendFCGIParam(dst []byte, name, value string) []byte {
	dst = appendFCGILength(dst, uint32(len(name)))
	dst = appendFCGILength(dst, uint32(len(value)))
	dst = append(dst, name...)
	return append(dst, value...)
}

func appendFCGILength(dst []byte, length uint32) []byte {
	if length < 128 {
		return append(dst, byte(length))
	}
	return append(dst, byte(length>>24)|0x80, byte(length>>16), byte(length>>8), byte(length))
}

func writeFCGIRecord(w io.Writer, requestID uint16, recordType byte, content []byte) error {
	if len(content) > 65535 {
		return errors.New("FastCGI record exceeds protocol limit")
	}
	header := []byte{1, recordType, byte(requestID >> 8), byte(requestID), byte(len(content) >> 8), byte(len(content)), 0, 0}
	if err := writeAll(w, header); err != nil {
		return err
	}
	if len(content) > 0 {
		if err := writeAll(w, content); err != nil {
			return err
		}
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func readFCGIResponse(r io.Reader, limit int) ([]byte, error) {
	var output []byte
	var header [8]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return nil, fmt.Errorf("read PHP-FPM FastCGI header: %w", err)
		}
		if header[0] != 1 || binary.BigEndian.Uint16(header[2:4]) != 1 {
			return nil, errors.New("PHP-FPM returned an invalid FastCGI record")
		}
		size := int(binary.BigEndian.Uint16(header[4:6]))
		padding := int(header[6])
		payload := make([]byte, size+padding)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("read PHP-FPM FastCGI payload: %w", err)
		}
		switch header[1] {
		case 6: // STDOUT
			if len(output)+size > limit {
				return nil, errors.New("PHP-FPM status response exceeds its size limit")
			}
			output = append(output, payload[:size]...)
		case 7: // STDERR; status detail must come from JSON stdout only.
		case 3: // END_REQUEST
			if size != 8 {
				return nil, errors.New("PHP-FPM returned a malformed FastCGI completion record")
			}
			if binary.BigEndian.Uint32(payload[:4]) != 0 || payload[4] != 0 {
				return nil, errors.New("PHP-FPM FastCGI request did not complete successfully")
			}
			return output, nil
		default:
			return nil, fmt.Errorf("PHP-FPM returned unsupported FastCGI record type %d", header[1])
		}
	}
}

func fastCGIBody(response []byte) ([]byte, error) {
	separator := []byte("\r\n\r\n")
	index := bytes.Index(response, separator)
	sepLength := len(separator)
	if index < 0 {
		separator = []byte("\n\n")
		index = bytes.Index(response, separator)
		sepLength = len(separator)
	}
	if index < 0 {
		return nil, errors.New("PHP-FPM status response has no CGI headers")
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(response[:index]), "\r", ""), "\n") {
		if strings.HasPrefix(strings.ToLower(line), "status:") {
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "status:")))
			if len(fields) == 0 || fields[0] != "200" {
				return nil, fmt.Errorf("PHP-FPM status endpoint returned %s", strings.TrimSpace(line))
			}
		}
	}
	body := bytes.TrimSpace(response[index+sepLength:])
	if len(body) == 0 {
		return nil, errors.New("PHP-FPM status response is empty")
	}
	return body, nil
}

func parseFPMUint(raw json.RawMessage) (uint64, error) {
	if len(raw) == 0 {
		return 0, errors.New("PHP-FPM status omitted a required numeric field")
	}
	var number uint64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		n, parseErr := strconv.ParseUint(text, 10, 64)
		if parseErr == nil {
			return n, nil
		}
	}
	return 0, errors.New("PHP-FPM status contains an invalid numeric field")
}
