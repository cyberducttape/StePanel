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

// Application kinds that own ports. A Node application's manifest and its
// rollback manifest belong to one application, so they may share a port.
const (
	appPortKindNode   = "node"
	appPortKindPython = "python"
)

// appPortOwner is one port claim found in the application state directory.
type appPortOwner struct {
	site, kind string
	rollback   bool
	port       int
}

// appPortOwners reads every Node manifest (<site>.json), Node rollback
// manifest (<site>.json.bak) and Python manifest (<site>-python.json). A
// file's site field must match the site its name implies, which also tells a
// Python manifest from the Node manifest of a site whose name ends -python.
func appPortOwners(cfg Config) ([]appPortOwner, error) {
	entries, err := os.ReadDir(cfg.AppRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect application manifests: %w", err)
	}
	var owners []appPortOwner
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !entry.Type().IsRegular() || !(strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".json.bak")) {
			continue
		}
		path, err := safePath(cfg.AppRoot, name)
		if err != nil {
			continue
		}
		data, err := readStateFile(path, maxAppStateFileBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read application manifest %s: %w", name, err)
		}
		var manifest struct {
			Site string `json:"site"`
			Port int    `json:"port"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			// An unreadable manifest names no port; deploy refuses to
			// overwrite it for its own site, so it cannot hide one here.
			log.Printf("application port check: skipping unparsable manifest %s: %v", name, err)
			continue
		}
		owner := appPortOwner{site: manifest.Site, kind: appPortKindNode, port: manifest.Port}
		switch {
		case strings.HasSuffix(name, ".json.bak"):
			owner.rollback = true
			if manifest.Site != strings.TrimSuffix(name, ".json.bak") {
				continue
			}
		case strings.HasSuffix(name, "-python.json") && manifest.Site == strings.TrimSuffix(name, "-python.json"):
			owner.kind = appPortKindPython
		case manifest.Site != strings.TrimSuffix(name, ".json"):
			continue
		}
		if safeUser(owner.site) != owner.site || owner.site == "" {
			continue
		}
		owners = append(owners, owner)
	}
	return owners, nil
}

// appPortConflict refuses a port that any other application names: another
// site's Node, Node rollback or Python manifest, or the same site's
// application of the other kind. One site can then never take over (or later
// roll back onto) another application's backend. Unless this application
// already runs there, it also refuses a port something else is listening on,
// such as the panel, a database or another service, because a proxy to the
// port would expose that listener.
func appPortConflict(cfg Config, site, kind string, port int) error {
	if safeUser(site) == "" || port < 1 || port > 65535 || (kind != appPortKindNode && kind != appPortKindPython) {
		return errors.New("invalid application site, kind or port")
	}
	owners, err := appPortOwners(cfg)
	if err != nil {
		return err
	}
	currentPort := 0
	for _, owner := range owners {
		if owner.site == site && owner.kind == kind {
			if !owner.rollback {
				currentPort = owner.port
			}
			continue
		}
		if owner.port == port {
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
