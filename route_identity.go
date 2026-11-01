package stepanel

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
)

// maxManagedRouteBytes bounds a managed route file read to find its domain.
const maxManagedRouteBytes = 1 << 20

var managedRouteDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// managedRouteServedDomain returns the domain a root-written route or proxy
// file serves. A route's file name alone does not identify its site:
// "alice-shop-example_org" is alice's shop-example.org proxy or alice-shop's
// example.org proxy. The served domain, which the helpers keep unique across
// managed routes, settles it. It reads the Apache/OpenLiteSpeed ServerName
// or vhDomain, the OpenLiteSpeed vhost/proxy header, or the Caddy site address.
func managedRouteServedDomain(path string) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("managed route %s is not a regular file", path)
	}
	scanner := bufio.NewScanner(io.LimitReader(file, maxManagedRouteBytes))
	scanner.Buffer(make([]byte, 0, 4096), maxManagedRouteBytes)
	caddyAddressSeen := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var candidate string
		switch {
		case strings.EqualFold(fields[0], "ServerName") && len(fields) == 2:
			candidate = fields[1]
		case fields[0] == "vhDomain" && len(fields) == 2:
			candidate = strings.Split(fields[1], ",")[0]
		case strings.HasPrefix(line, "# StePanel managed OpenLiteSpeed ") && len(fields) == 7 && fields[5] == "for":
			candidate = fields[6]
		case strings.HasPrefix(line, "#"):
			continue
		case !caddyAddressSeen && len(fields) == 2 && fields[1] == "{":
			// A Caddy site block opens with its address.
			caddyAddressSeen = true
			candidate = fields[0]
		default:
			caddyAddressSeen = true
			continue
		}
		domain := strings.ToLower(candidate)
		if !managedRouteDomain.MatchString(domain) {
			return "", fmt.Errorf("managed route %s serves an invalid domain", path)
		}
		return domain, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read managed route %s: %w", path, err)
	}
	return "", errors.New("managed route " + path + " names no served domain")
}
