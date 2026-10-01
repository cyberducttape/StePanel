// Package siteidentity is the single Go definition of a managed site's Unix
// identity. It must stay byte-for-byte compatible with the derivation in
// deploy/integrations/stepanel-sitectl, because a site created by one
// implementation is later chowned, reconfigured, and deleted by the other.
package siteidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// UnixUser returns the system account that owns a site's files and runs its
// PHP pool: "sp-" + the first 18 characters of the site name with "_"
// replaced by "-" + "-" + the first 8 hex digits of sha256(site).
//
// The hash suffix keeps names unique after truncation and "_"→"-" folding,
// and keeps the result within the 32-character useradd limit.
func UnixUser(site string) string {
	sum := sha256.Sum256([]byte(site))
	prefix := site
	if len(prefix) > 18 {
		prefix = prefix[:18]
	}
	prefix = strings.ReplaceAll(prefix, "_", "-")
	return "sp-" + prefix + "-" + hex.EncodeToString(sum[:])[:8]
}

// ErrNoWebGroup reports that neither supported web server group exists.
var ErrNoWebGroup = errors.New("neither the www-data nor the apache group exists")

// WebGroup selects the web server group the same way stepanel-sitectl does:
// www-data (Debian/Ubuntu) when present, otherwise apache (RHEL family).
// groupExists reports whether a group name resolves on the host.
func WebGroup(groupExists func(name string) bool) (string, error) {
	for _, candidate := range []string{"www-data", "apache"} {
		if groupExists(candidate) {
			return candidate, nil
		}
	}
	return "", ErrNoWebGroup
}
