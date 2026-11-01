package rootbroker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/cyberducttape/StePanel/internal/operations"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// Fencing scope binds a request's leases to the exact resources it mutates.
// A token proves only that its holder owns some live lease; the broker must
// also check that the lease is for this request's target, or a caller holding
// a lease on site A could fence a mutation of site B and defeat the
// serialization and stale-writer protection the leases exist for.
//
// The acceptable lease keys are derived here from the validated request,
// never from anything the caller declares about its scope. The keys mirror
// the panel's lock names: a bare site name, "account:<user>", "vhost:<name>",
// "proxy:<name>", "database:<name>", "database:session:<id>",
// "database:reconcile", and "certificate:<domain>".

// defaultDatabaseRegistryRoot is the root-owned registry stepanel-dbctl keeps
// for managed databases; each record names the site that owns the database.
const defaultDatabaseRegistryRoot = "/var/lib/stepanel-privileged/db-managed"

// maxDatabaseRegistryRecord bounds a registry record read by the broker.
const maxDatabaseRegistryRecord = 4096

var registryDatabaseName = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// fencingScope is the set of lease keys that may admit one request.
type fencingScope struct {
	keys    map[string]bool
	targets []string
}

func (s *fencingScope) add(keys ...string) {
	if s.keys == nil {
		s.keys = map[string]bool{}
	}
	for _, key := range keys {
		if key != "" {
			s.keys[key] = true
		}
	}
}

func (s *fencingScope) target(description string) {
	s.targets = append(s.targets, description)
}

// covers reports whether any supplied token is for an acceptable key.
func (s fencingScope) covers(tokens []operations.FencingToken) bool {
	for _, token := range tokens {
		if s.keys[token.ResourceKey] {
			return true
		}
	}
	return false
}

func (s fencingScope) describe() string {
	if len(s.targets) == 0 {
		return "no fencable resource"
	}
	return strings.Join(s.targets, ", ")
}

// checkFencingScope rejects tokens that do not cover the request's target and
// conflicting tokens for one resource. It runs after each token has been
// verified as a live lease.
func (b *Broker) checkFencingScope(req *Request) error {
	seen := map[string]operations.FencingToken{}
	for _, token := range req.Fencing {
		if previous, ok := seen[token.ResourceKey]; ok && previous != token {
			return fmt.Errorf("conflicting fencing tokens for %s", token.ResourceKey)
		}
		seen[token.ResourceKey] = token
	}
	scope, err := b.fencingScopeFor(req)
	if err != nil {
		return err
	}
	if len(scope.keys) == 0 {
		return fmt.Errorf("request names %s; refusing a fenced mutation without a target", scope.describe())
	}
	if !scope.covers(req.Fencing) {
		held := make([]string, 0, len(req.Fencing))
		for _, token := range req.Fencing {
			held = append(held, token.ResourceKey)
		}
		sort.Strings(held)
		return fmt.Errorf("fencing lease for %s does not cover %s", strings.Join(held, ", "), scope.describe())
	}
	return nil
}

// databaseCreatingActions create a database for the request's site and
// register it to that site; stepanel-dbctl refuses them for a database that
// already exists or is registered.
var databaseCreatingActions = map[string]bool{"provision": true, "restore": true, "restore-wordpress": true}

// fencingScopeFor derives the acceptable lease keys for a validated request.
func (b *Broker) fencingScopeFor(req *Request) (fencingScope, error) {
	var scope fencingScope
	var err error
	addSite := func(site string) {
		if err == nil && site != "" {
			err = b.addSiteScope(&scope, site)
		}
	}
	switch req.RequestType {
	case "site":
		addSite(req.Site.Site)
	case "app":
		addSite(req.App.Site)
	case "worker":
		addSite(req.Worker.Site)
	case "runner":
		addSite(req.Runner.Site)
	case "environment":
		addSite(req.Environment.Site)
	case "task":
		addSite(req.Task.Site)
	case "wordpress":
		addSite(req.WordPress.Site)
	case "files":
		addSite(req.Files.Site)
	case "git":
		addSite(req.Git.Site)
	case "db":
		// The database (or session) the helper acts on decides the scope;
		// the request's site field is only a claim. A site's lease admits a
		// database operation when the root-owned registry records that site
		// as the database's owner, or when the action creates the database
		// for that site (the helper refuses a database that already exists
		// or is registered, and records the site as its owner).
		db := req.DB
		switch {
		case db.Action == "reconcile":
			scope.add("database:reconcile")
			scope.target("database reconcile")
		case db.SessionID != "":
			scope.add("database:session:" + db.SessionID)
			scope.target("database session " + db.SessionID)
		case db.Database != "":
			scope.add("database:" + db.Database)
			scope.target("database " + db.Database)
			owner, ownerErr := b.databaseRegistrySite(db.Database)
			if ownerErr != nil {
				return fencingScope{}, ownerErr
			}
			switch {
			case owner != "" && db.Site != "" && db.Site != owner:
				return fencingScope{}, fmt.Errorf("database %s is registered to site %s, not %s", db.Database, owner, db.Site)
			case owner != "":
				addSite(owner)
			case databaseCreatingActions[db.Action]:
				addSite(db.Site)
			}
		default:
			addSite(db.Site)
		}
	case "vhost":
		vhost := req.Vhost
		if vhost.Action == "delete" {
			// Deletion names a file, which never proves its owner by its
			// shape (site-alice-shop-example_org is alice's shop-example.org
			// or alice-shop's example.org). Only the exact route lease, or
			// the lease of the site and domain the validator matched to the
			// file's canonical name, admits it.
			scope.add("vhost:" + vhost.Name)
			scope.target("vhost " + vhost.Name)
			addSite(vhost.Site)
		} else {
			addSite(vhost.Site)
		}
	case "proxy":
		proxy := req.Proxy
		switch proxy.Action {
		case "delete":
			scope.add("proxy:" + proxy.Name)
			scope.target("proxy " + proxy.Name)
			addSite(proxy.Site)
		case "reload":
			addSite(proxy.Site)
			scope.add("proxy:reload")
			scope.target("proxy reload")
		default:
			addSite(proxy.Site)
		}
	case "account":
		account := req.Account
		scope.add("account:"+account.Account, account.Account)
		scope.target("account " + account.Account)
		// Creating a site's account precedes its placement record; ensure
		// only creates the account's identity idempotently.
		err = b.addAccountSiteScope(&scope, account.Account, account.Site, account.Action == "ensure")
	case "resource":
		resource := req.Resource
		if resource.Action == "apply-account" {
			scope.add("account:"+resource.Account, resource.Account)
			scope.target("account " + resource.Account)
			err = b.addAccountSiteScope(&scope, resource.Account, resource.Site, false)
		} else {
			// The site's own lease, or its placement account's; the
			// request's account field never widens the scope.
			addSite(resource.Site)
		}
	case "certificate":
		scope.add("certificate:" + strings.ToLower(req.Certificate.Domain))
		scope.target("certificate " + strings.ToLower(req.Certificate.Domain))
	default:
		return fencingScope{}, fmt.Errorf("no fencing scope is defined for %s requests", req.RequestType)
	}
	if err != nil {
		return fencingScope{}, err
	}
	return scope, nil
}

// addAccountSiteScope admits, for an account-wide request made on behalf of
// one site, that site's lease, but only when the site is placed in that very
// account (or, with allowUnplaced, not placed yet): a site's lease must never
// admit a change to another account.
func (b *Broker) addAccountSiteScope(scope *fencingScope, account, site string, allowUnplaced bool) error {
	if site == "" {
		return nil
	}
	placement, err := b.sitePlacement(site)
	switch {
	case err == nil && placement.Account == account:
		return b.addSiteScope(scope, site)
	case err == nil:
		return nil
	case errors.Is(err, sitelayout.ErrNotPlaced):
		if allowUnplaced {
			return b.addSiteScope(scope, site)
		}
		return nil
	default:
		return err
	}
}

// addSiteScope admits the site's own lease and, for a site placed in a
// hosting account, the account's lease: account-wide operations lock the
// account (or its main site, named after the account) rather than each site.
func (b *Broker) addSiteScope(scope *fencingScope, site string) error {
	scope.add(site)
	scope.target("site " + site)
	placement, err := b.sitePlacement(site)
	switch {
	case err == nil:
		scope.add("account:"+placement.Account, placement.Account)
	case errors.Is(err, sitelayout.ErrNotPlaced):
	default:
		return err
	}
	return nil
}

func (b *Broker) sitePlacement(site string) (sitelayout.Placement, error) {
	if b.fencingDB == nil {
		return sitelayout.Placement{}, sitelayout.ErrNotPlaced
	}
	placement, err := sitelayout.Get(b.fencingDB, site)
	if err != nil && !errors.Is(err, sitelayout.ErrNotPlaced) {
		return sitelayout.Placement{}, fmt.Errorf("resolve fencing scope for site %s: %w", site, err)
	}
	return placement, err
}

var fencingSiteName = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// databaseRegistrySite returns the site recorded as the owner of a managed
// database by stepanel-dbctl, or "" when the database is not registered or
// its record names no site (for example, a WordPress proof database is owned
// by its disposable proof site).
func (b *Broker) databaseRegistrySite(database string) (string, error) {
	if !registryDatabaseName.MatchString(database) {
		return "", nil
	}
	root := b.databaseRegistryRoot
	if root == "" {
		root = defaultDatabaseRegistryRoot
	}
	path := filepath.Join(root, database)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read database ownership record: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("database ownership record for %s is not a regular file", database)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read database ownership record: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDatabaseRegistryRecord))
	if err != nil {
		return "", fmt.Errorf("read database ownership record: %w", err)
	}
	return registryRecordSite(string(data)), nil
}

// registryRecordSite mirrors stepanel-dbctl's record_site: records are
// "[pending-]{cpmove,wordpress,panel}:<site>[:...]".
func registryRecordSite(record string) string {
	value := strings.TrimSpace(strings.SplitN(record, "\n", 2)[0])
	value = strings.TrimPrefix(value, "pending-")
	kind, rest, ok := strings.Cut(value, ":")
	if !ok {
		return ""
	}
	switch kind {
	case "cpmove", "wordpress", "panel":
	default:
		return ""
	}
	site, tail, hasTail := strings.Cut(rest, ":")
	// panel and wordpress records are KIND:SITE:USER; a legacy
	// wordpress:USER record names no site, only the database user.
	if kind != "cpmove" && !hasTail || kind != "cpmove" && tail == "" {
		return ""
	}
	if !fencingSiteName.MatchString(site) {
		return ""
	}
	return site
}
