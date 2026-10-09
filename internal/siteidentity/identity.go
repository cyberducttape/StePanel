// Package siteidentity is the single Go definition of a managed site's Unix
// identity. It must stay byte-for-byte compatible with the derivation in
// deploy/integrations/stepanel-sitectl, because a site created by one
// implementation is later chowned, reconfigured, and deleted by the other.
package siteidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
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

var webGroupLine = regexp.MustCompile(`^STEPANEL_WEB_GROUP="([a-z_][a-z0-9_-]*)"$`)

// WebGroupFromEnv reads the installer-owned web-server group from the
// root-owned environment file. Production callers must use this value rather
// than independently guessing from whichever distribution groups happen to
// exist on the host.
func WebGroupFromEnv(path string, groupExists func(name string) bool) (string, error) {
	if path == "" {
		return "", errors.New("web group configuration path is empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect web group configuration: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("web group configuration must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("web group configuration must not be group or world accessible")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read web group configuration: %w", err)
	}
	var group string
	for _, line := range strings.Split(string(data), "\n") {
		matches := webGroupLine.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
		if len(matches) == 0 {
			continue
		}
		if group != "" && group != matches[1] {
			return "", errors.New("web group configuration contains conflicting values")
		}
		group = matches[1]
	}
	if group == "" {
		return "", errors.New("web group configuration does not define STEPANEL_WEB_GROUP")
	}
	if groupExists == nil || !groupExists(group) {
		return "", fmt.Errorf("configured web group %q does not exist", group)
	}
	return group, nil
}
