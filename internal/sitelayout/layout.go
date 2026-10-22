// Package sitelayout places sites inside hosting-account homes (ADR-0007).
//
// A hosting account is a Unix user whose home is <homeRoot>/<user>. A site is
// one document root inside that home: the account's main site is served from
// public_html, and each addon domain or subdomain from a directory named
// after the domain, next to public_html. Sites without a placement keep the
// legacy per-site layout under the web root.
package sitelayout

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Kind is the role a site plays within its account.
type Kind string

const (
	KindMain      Kind = "main"
	KindAddon     Kind = "addon"
	KindSubdomain Kind = "subdomain"
)

// MainDirectory is the document root directory of an account's main site.
const MainDirectory = "public_html"

// ManagedGroup is the root-managed group every hosting-account user belongs
// to. Privileged helpers refuse to act on a user outside it.
const ManagedGroup = "stepanel-users"

// Placement locates one site inside an account home.
type Placement struct {
	Site      string
	Account   string
	UnixUser  string
	Kind      Kind
	Directory string
}

var (
	unixUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	sitePattern     = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	domainPattern   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// reservedUsers are names a hosting account may never take: system and
// service accounts that commonly exist on supported distributions, names a
// cPanel host also reserves, and StePanel's own identities.
var reservedUsers = map[string]bool{
	"adm": true, "admin": true, "administrator": true, "apache": true, "avahi": true,
	"backup": true, "bin": true, "caddy": true, "chrony": true, "clamav": true,
	"cpanel": true, "daemon": true, "dbus": true, "dovecot": true, "dovenull": true,
	"exim": true, "fail2ban": true, "ftp": true, "games": true, "gnats": true,
	"halt": true, "irc": true, "list": true, "lp": true, "lsadm": true, "mail": true,
	"man": true, "messagebus": true, "mysql": true, "named": true, "news": true,
	"nginx": true, "nobody": true, "ntp": true, "operator": true, "polkitd": true,
	"postfix": true, "postgres": true, "proxy": true, "redis": true, "root": true,
	"rpc": true, "rpcuser": true, "shutdown": true, "sshd": true, "stepanel": true,
	"sync": true, "sys": true, "syslog": true, "tcpdump": true, "tss": true,
	"uucp": true, "vsftpd": true, "www": true, "www-data": true,
}

// ValidUnixUser reports whether name can be a hosting-account Unix user: a
// portable login name that is not reserved and cannot be mistaken for a
// legacy per-site account (sp-*) or a systemd-owned user.
func ValidUnixUser(name string) error {
	switch {
	case !unixUserPattern.MatchString(name):
		return fmt.Errorf("account name %q must start with a lowercase letter or underscore and use at most 32 lowercase letters, digits, dashes and underscores", name)
	case reservedUsers[name]:
		return fmt.Errorf("account name %q is reserved for a system or service account", name)
	case strings.HasPrefix(name, "sp-"), strings.HasPrefix(name, "stepanel"), strings.HasPrefix(name, "systemd-"):
		return fmt.Errorf("account name %q uses a prefix reserved for StePanel or system accounts", name)
	}
	return nil
}

// ValidDomain reports whether domain is a lowercase DNS name that can name an
// addon or subdomain document root.
func ValidDomain(domain string) bool {
	return len(domain) <= 253 && domainPattern.MatchString(domain)
}

// Validate checks every field and the kind/directory pairing.
func (p Placement) Validate() error {
	if !sitePattern.MatchString(p.Site) {
		return fmt.Errorf("invalid site name %q", p.Site)
	}
	if err := ValidUnixUser(p.UnixUser); err != nil {
		return err
	}
	if p.Account == "" {
		return errors.New("placement requires an owning account")
	}
	switch p.Kind {
	case KindMain:
		if p.Directory != MainDirectory {
			return fmt.Errorf("a main site is served from %s, not %q", MainDirectory, p.Directory)
		}
	case KindAddon, KindSubdomain:
		if !ValidDomain(p.Directory) {
			return fmt.Errorf("%s site directory %q must be its domain name", p.Kind, p.Directory)
		}
	default:
		return fmt.Errorf("unknown site kind %q", p.Kind)
	}
	return nil
}

// Home is the account's home directory.
func Home(homeRoot, unixUser string) string {
	return filepath.Join(homeRoot, unixUser)
}

// DocRoot is the directory the web server serves for the site.
func (p Placement) DocRoot(homeRoot string) string {
	return filepath.Join(homeRoot, p.UnixUser, p.Directory)
}

// StagingRoot is where the site manager prepares trees for this layout. It
// sits on the same filesystem as the homes so activation is an atomic rename.
func StagingRoot(homeRoot string) string {
	return filepath.Join(homeRoot, ".stepanel-manager-staging")
}
