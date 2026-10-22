package stepanel

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// A cPanel backup records its domains in homedir-independent metadata:
// userdata/main lists the main, addon, sub and parked domains, and
// userdata/<domain> holds each domain's document root. A restore into a
// hosting account (ADR-0007) serves the main domain from public_html and
// recreates every addon and subdomain as its own site at /home/<user>/<domain>.

const (
	maxCPMoveUserdataBytes = 1 << 20
	maxCPMoveDomains       = 500
)

// cpmoveDomainPlan is how a backup's domains map onto the account layout.
type cpmoveDomainPlan struct {
	MainDomain string
	Parked     []string
	Sites      []cpmoveDomainSite
	// MainExcludes are directories under public_html that are document roots
	// of addon or subdomain sites (the classic layout); the main site does
	// not keep a second copy of them.
	MainExcludes map[string]bool
}

// cpmoveDomainSite is one addon or subdomain to restore as its own site.
type cpmoveDomainSite struct {
	Domain string
	Kind   sitelayout.Kind
	// Source is the domain's document root inside the extracted archive, or
	// "" when the backup has no files for it.
	Source string
}

// cpmoveUserdataMain is the subset of userdata/main a restore uses.
type cpmoveUserdataMain struct {
	MainDomain string
	Addons     map[string]string // addon domain -> its alias subdomain
	Subdomains []string
	Parked     []string
}

// parseCPMoveUserdataMain reads the YAML subset cPanel writes to
// userdata/main: top-level scalars, string lists and string maps. Anything
// else is ignored; malformed or oversized input is an error.
func parseCPMoveUserdataMain(data []byte) (cpmoveUserdataMain, error) {
	parsed := cpmoveUserdataMain{Addons: map[string]string{}}
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 4096), 64<<10)
	unquote := func(value string) string {
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '\'' && value[len(value)-1] == '\'' || value[0] == '"' && value[len(value)-1] == '"') {
			value = value[1 : len(value)-1]
		}
		return strings.ToLower(strings.TrimSpace(value))
	}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "---" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			key, value, found := strings.Cut(trimmed, ":")
			if !found {
				return cpmoveUserdataMain{}, errors.New("userdata/main has a malformed top-level line")
			}
			section = strings.TrimSpace(key)
			if section == "main_domain" {
				parsed.MainDomain = unquote(value)
			}
			continue
		}
		switch section {
		case "sub_domains", "parked_domains":
			item, found := strings.CutPrefix(trimmed, "- ")
			if !found {
				continue
			}
			if section == "sub_domains" {
				parsed.Subdomains = append(parsed.Subdomains, unquote(item))
			} else {
				parsed.Parked = append(parsed.Parked, unquote(item))
			}
		case "addon_domains":
			addon, alias, found := strings.Cut(trimmed, ":")
			if found {
				parsed.Addons[unquote(addon)] = unquote(alias)
			}
		}
		if len(parsed.Subdomains)+len(parsed.Parked)+len(parsed.Addons) > maxCPMoveDomains {
			return cpmoveUserdataMain{}, fmt.Errorf("backup lists more than %d domains", maxCPMoveDomains)
		}
	}
	if err := scanner.Err(); err != nil {
		return cpmoveUserdataMain{}, fmt.Errorf("read userdata/main: %w", err)
	}
	return parsed, nil
}

// cpmoveDocumentRoot reads userdata/<name> and returns the domain's document
// root relative to the account home, or "" when the backup does not record
// one. A document root outside the account's home is an error.
func cpmoveDocumentRoot(root, user, name string) (string, error) {
	data, err := readStateFile(filepath.Join(root, "userdata", name), maxCPMoveUserdataBytes)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read userdata for %s: %w", name, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found || strings.TrimSpace(key) != "documentroot" {
			continue
		}
		docroot := filepath.Clean(strings.Trim(strings.TrimSpace(value), `'"`))
		// cPanel homes may live in /home, /home2, ...: accept /<base>/<user>/<rel>.
		parts := strings.SplitN(strings.TrimPrefix(docroot, "/"), "/", 3)
		if !filepath.IsAbs(docroot) || len(parts) < 2 || parts[1] != user {
			return "", fmt.Errorf("document root of %s is outside the account home", name)
		}
		if len(parts) == 2 {
			return ".", nil
		}
		rel := filepath.Clean(parts[2])
		if rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			return "", fmt.Errorf("document root of %s escapes the account home", name)
		}
		return rel, nil
	}
	return "", nil
}

// planCPMoveDomains maps a backup's domains onto the account layout. A
// backup without userdata/main restores only the main site.
func planCPMoveDomains(root, user string) (cpmoveDomainPlan, error) {
	plan := cpmoveDomainPlan{MainExcludes: map[string]bool{}}
	data, err := readStateFile(filepath.Join(root, "userdata", "main"), maxCPMoveUserdataBytes)
	if errors.Is(err, os.ErrNotExist) {
		return plan, nil
	}
	if err != nil {
		return plan, fmt.Errorf("read userdata/main: %w", err)
	}
	main, err := parseCPMoveUserdataMain(data)
	if err != nil {
		return plan, err
	}
	if main.MainDomain != "" {
		if err := domainname.Validate(main.MainDomain); err != nil {
			return plan, fmt.Errorf("backup main domain: %w", err)
		}
		plan.MainDomain = main.MainDomain
	}
	for _, parked := range main.Parked {
		if domainname.Validate(parked) == nil {
			plan.Parked = append(plan.Parked, parked)
		}
	}
	homedir := filepath.Join(root, "homedir")
	aliases := map[string]bool{}
	add := func(domain, userdataName string, kind sitelayout.Kind) error {
		if domainname.Validate(domain) != nil || !sitelayout.ValidDomain(domain) {
			return fmt.Errorf("backup domain %q is not a valid document root name", domain)
		}
		rel, err := cpmoveDocumentRoot(root, user, userdataName)
		if err != nil {
			return err
		}
		site := cpmoveDomainSite{Domain: domain, Kind: kind}
		if rel != "" && rel != "." && rel != sitelayout.MainDirectory {
			source, err := safePath(homedir, rel)
			if err != nil {
				return fmt.Errorf("document root of %s: %w", domain, err)
			}
			if info, err := os.Lstat(source); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				site.Source = source
				if nested, found := strings.CutPrefix(rel, sitelayout.MainDirectory+"/"); found {
					plan.MainExcludes[nested] = true
				}
			}
		}
		plan.Sites = append(plan.Sites, site)
		return nil
	}
	addons := make([]string, 0, len(main.Addons))
	for addon := range main.Addons {
		addons = append(addons, addon)
	}
	sort.Strings(addons)
	for _, addon := range addons {
		alias := main.Addons[addon]
		aliases[alias] = true
		// An addon domain is implemented as a subdomain alias; its document
		// root is recorded under the alias.
		source := alias
		if source == "" {
			source = addon
		}
		if err := add(addon, source, sitelayout.KindAddon); err != nil {
			return plan, err
		}
	}
	subdomains := append([]string(nil), main.Subdomains...)
	sort.Strings(subdomains)
	for _, sub := range subdomains {
		if aliases[sub] || sub == "" {
			continue
		}
		if err := add(sub, sub, sitelayout.KindSubdomain); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

// siteNameForDomain derives a site name from a domain: dots become dashes,
// truncated to the site-name limit, with a numeric suffix when taken.
func siteNameForDomain(domain string, taken func(string) bool) (string, error) {
	base := strings.Trim(strings.ReplaceAll(strings.ToLower(domain), ".", "-"), "-")
	if len(base) > 32 {
		base = strings.TrimRight(base[:32], "-")
	}
	if base == "" {
		return "", fmt.Errorf("cannot derive a site name from %q", domain)
	}
	for attempt := 1; attempt <= 99; attempt++ {
		name := base
		if attempt > 1 {
			suffix := fmt.Sprintf("-%d", attempt)
			trimmed := base
			if len(trimmed)+len(suffix) > 32 {
				trimmed = strings.TrimRight(trimmed[:32-len(suffix)], "-")
			}
			name = trimmed + suffix
		}
		if safeUser(name) == name && !taken(name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no free site name for %s", domain)
}

// restoreCPMoveDomainSites restores each planned addon and subdomain as its
// own site in the account. The main site is already committed; a domain that
// cannot be restored is reported and fully cleaned up, never half-created.
func restoreCPMoveDomainSites(ctx context.Context, cfg Config, db *sql.DB, manager siteauthority.Manager, account string, plan cpmoveDomainPlan) ([]ImportedDomain, []string) {
	var restored []ImportedDomain
	var failures []string
	existing, err := sitelayout.ForAccount(db, account)
	if err != nil {
		return nil, []string{"list account sites: " + err.Error()}
	}
	used := map[string]string{}
	for _, placement := range existing {
		used[placement.Directory] = placement.Site
	}
	taken := func(name string) bool {
		if _, err := sitelayout.Get(db, name); err == nil {
			return true
		}
		if _, err := existingManagedSiteRoot(cfg.WebRoot, name); err == nil {
			return true
		}
		return name == account
	}
	for _, domain := range plan.Sites {
		if err := ctx.Err(); err != nil {
			failures = append(failures, domain.Domain+": "+err.Error())
			continue
		}
		if site, found := used[domain.Domain]; found {
			failures = append(failures, fmt.Sprintf("%s: already served by site %s; not overwritten", domain.Domain, site))
			continue
		}
		name, err := siteNameForDomain(domain.Domain, taken)
		if err != nil {
			failures = append(failures, domain.Domain+": "+err.Error())
			continue
		}
		placement := sitelayout.Placement{Site: name, Account: account, UnixUser: account, Kind: domain.Kind, Directory: domain.Domain}
		if err := restoreCPMoveDomainSite(ctx, cfg, db, manager, placement, domain.Source); err != nil {
			failures = append(failures, domain.Domain+": "+err.Error())
			continue
		}
		used[domain.Domain] = name
		restored = append(restored, ImportedDomain{Domain: domain.Domain, Site: name, Kind: string(domain.Kind), DocumentRoot: placement.DocRoot(cfg.HomeRoot)})
	}
	return restored, failures
}

func restoreCPMoveDomainSite(ctx context.Context, cfg Config, db *sql.DB, manager siteauthority.Manager, placement sitelayout.Placement, source string) (err error) {
	placedNew, err := placeSiteInAccount(ctx, cfg, db, placement)
	if err != nil {
		return err
	}
	prepared, activated := false, false
	defer func() {
		if err == nil {
			return
		}
		cleanupCtx := context.WithoutCancel(ctx)
		if prepared || activated {
			if deleteErr := siteHelperContext(cleanupCtx, cfg, "delete", placement.Site); deleteErr != nil {
				log.Printf("remove partially restored domain site %s: %v", placement.Site, deleteErr)
			} else if deleteErr := manager.Delete(cleanupCtx, placement.Site); deleteErr != nil {
				log.Printf("finalize removal of partially restored domain site %s: %v", placement.Site, deleteErr)
			}
		}
		if placedNew {
			if unplaceErr := unplaceSite(cleanupCtx, cfg, db, placement.Site); unplaceErr != nil {
				log.Printf("forget placement of partially restored domain site %s: %v", placement.Site, unplaceErr)
			}
		}
	}()
	stage, owner, err := manager.CreateSiteStagingLease(ctx, placement.Site, ".stepanel-cpmove-")
	if err != nil {
		return fmt.Errorf("create staging: %w", err)
	}
	defer func() {
		if !activated {
			logStagingDiscardFailure(stage, manager.DiscardStaging(context.Background(), stage))
		}
		if releaseErr := manager.ReleaseStagingLease(stage, owner); releaseErr != nil {
			log.Printf("release cpmove domain staging lease for %s: %v", placement.Site, releaseErr)
		}
	}()
	if source != "" {
		if findings, scanErr := scanPHP(source); scanErr != nil {
			return fmt.Errorf("suspicious PHP heuristic scan failed: %w", scanErr)
		} else if len(findings) > 0 {
			return fmt.Errorf("restore blocked: suspicious PHP heuristic scan found %d file(s) requiring review", len(findings))
		}
		if err := copyTreeContext(ctx, source, stage); err != nil {
			return fmt.Errorf("copy document root: %w", err)
		}
	}
	prepared = true
	if err := siteHelperContext(ctx, cfg, "prepare", placement.Site); err != nil {
		return fmt.Errorf("prepare site: %w", err)
	}
	if _, err := manager.ActivateStaged(ctx, placement.Site, stage); err != nil {
		return fmt.Errorf("activate site: %w", err)
	}
	activated = true
	if err := siteHelperContext(ctx, cfg, "seal", placement.Site); err != nil {
		return fmt.Errorf("seal site: %w", err)
	}
	return nil
}
