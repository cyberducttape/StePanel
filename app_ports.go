package stepanel

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// errAppPortTaken is returned when an application may not use a port.
var errAppPortTaken = errors.New("application port is not available")

// appPortListening reports whether something accepts connections on the
// loopback port. Tests replace it so they do not depend on the host.
var appPortListening = realAppPortListening

func realAppPortListening(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
		if err == nil {
			_ = conn.Close() // best-effort: probe connection carries no data
			return true
		}
	}
	return false
}

// appPortConflict refuses a port another site's application manifest or
// rollback manifest names, so one site can never take over (or later roll
// back onto) another site's backend. Unless site already runs there, it also
// refuses a port something else is listening on, such as the panel, a
// database or another service, because the site's proxy would then expose
// that listener under the site's domain.
func appPortConflict(cfg Config, site string, port int) error {
	if safeUser(site) == "" || port < 1 || port > 65535 {
		return errors.New("invalid application site or port")
	}
	entries, err := os.ReadDir(cfg.AppRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect application manifests: %w", err)
	}
	currentPort := 0
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !entry.Type().IsRegular() {
			continue
		}
		var other, suffix string
		switch {
		case strings.HasSuffix(name, ".json.bak"):
			other, suffix = strings.TrimSuffix(name, ".json.bak"), ".json.bak"
		case strings.HasSuffix(name, ".json"):
			other, suffix = strings.TrimSuffix(name, ".json"), ".json"
		default:
			continue
		}
		if safeUser(other) != other {
			continue
		}
		data, ok, err := readAppStateFile(cfg, other, suffix)
		if err != nil {
			return fmt.Errorf("read application manifest %s: %w", name, err)
		}
		if !ok {
			continue
		}
		var manifest AppManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			// An unreadable manifest names no port; deploy refuses to
			// overwrite it for its own site, so it cannot hide one here.
			log.Printf("application port check: skipping unparsable manifest %s: %v", name, err)
			continue
		}
		if other == site {
			if suffix == ".json" {
				currentPort = manifest.Port
			}
			continue
		}
		if manifest.Port == port {
			return fmt.Errorf("%w: port %d is assigned to another application", errAppPortTaken, port)
		}
	}
	if port != currentPort && appPortListening(port) {
		return fmt.Errorf("%w: port %d is already in use", errAppPortTaken, port)
	}
	return nil
}

// customerNodeBackendAllowed limits a customer's Node deployment proxy to the
// application it deploys: loopback on the application's own port. Only an
// administrator may point a site's domain at another local or private
// backend.
func customerNodeBackendAllowed(backend string, port int) error {
	normalized, err := localBackend(backend)
	if err != nil {
		return err
	}
	host, backendPort, err := net.SplitHostPort(normalized)
	if err != nil {
		return errors.New("backend must include a host and port")
	}
	if backendPort != strconv.Itoa(port) {
		return fmt.Errorf("backend must use the application port %d", port)
	}
	if host != "localhost" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return errors.New("backend must be the application on localhost")
		}
	}
	return nil
}
