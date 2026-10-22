package stepanel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// Hosting accounts (ADR-0007): an account is a Unix user with a home under
// Config.HomeRoot, and each of its sites is a document root inside that home.

// accountHelperContext runs one hosting-account operation through the root
// broker. Development runs have no privileged helpers, so they only create
// the directories the site manager publishes into.
func accountHelperContext(ctx context.Context, cfg Config, req rootbroker.AccountRequest) (rootbroker.AccountResponse, error) {
	if cfg.Production || labDirectRootBrokerEnabled() {
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
		if err != nil {
			return rootbroker.AccountResponse{}, err
		}
		return client.AccountRequest(ctx, req)
	}
	switch req.Action {
	case "ensure":
		home, err := safePath(cfg.HomeRoot, req.Account)
		if err != nil {
			return rootbroker.AccountResponse{}, err
		}
		if err := os.MkdirAll(home, 0o711); err != nil {
			return rootbroker.AccountResponse{}, fmt.Errorf("create development account home: %w", err)
		}
	case "place":
		private, err := safePath(cfg.HomeRoot, req.Account, ".stepanel", "sites", req.Site, "php")
		if err != nil {
			return rootbroker.AccountResponse{}, err
		}
		if err := os.MkdirAll(private, 0o750); err != nil {
			return rootbroker.AccountResponse{}, fmt.Errorf("create development site state: %w", err)
		}
	}
	return rootbroker.AccountResponse{UnixUser: req.Account}, nil
}

// sitePlacementRequest describes where a new site goes inside an account.
type sitePlacementRequest struct {
	Account string
	Kind    sitelayout.Kind
	Domain  string
}

// resolveSitePlacement validates a requested placement and fills in the main
// site when the account has none yet. An addon or subdomain site needs its
// domain, which also names its document root directory.
func resolveSitePlacement(db *sql.DB, site string, req sitePlacementRequest) (sitelayout.Placement, error) {
	if err := sitelayout.ValidUnixUser(req.Account); err != nil {
		return sitelayout.Placement{}, err
	}
	kind := req.Kind
	if kind == "" {
		existing, err := sitelayout.ForAccount(db, req.Account)
		if err != nil {
			return sitelayout.Placement{}, err
		}
		kind = sitelayout.KindMain
		for _, placement := range existing {
			if placement.Kind == sitelayout.KindMain && placement.Site != site {
				kind = sitelayout.KindAddon
			}
		}
	}
	placement := sitelayout.Placement{Site: site, Account: req.Account, UnixUser: req.Account, Kind: kind}
	switch kind {
	case sitelayout.KindMain:
		placement.Directory = sitelayout.MainDirectory
	case sitelayout.KindAddon, sitelayout.KindSubdomain:
		domain := strings.ToLower(strings.TrimSpace(req.Domain))
		if domain == "" {
			return sitelayout.Placement{}, errors.New("an addon or subdomain site needs its domain, which names its document root")
		}
		if err := domainname.Validate(domain); err != nil {
			return sitelayout.Placement{}, err
		}
		placement.Directory = domain
	default:
		return sitelayout.Placement{}, fmt.Errorf("unknown site kind %q", kind)
	}
	return placement, placement.Validate()
}

// placeSiteInAccount allocates the account's Unix user, makes sure it exists
// on the host, and records and publishes the site's placement. It is
// idempotent so a retried creation job resumes where it stopped, and reports
// whether this call recorded the placement.
func placeSiteInAccount(ctx context.Context, cfg Config, db *sql.DB, placement sitelayout.Placement) (bool, error) {
	if _, err := sitelayout.AllocateAccount(db, placement.Account); err != nil {
		return false, fmt.Errorf("allocate hosting account %s: %w", placement.Account, err)
	}
	if _, err := accountHelperContext(ctx, cfg, rootbroker.AccountRequest{Action: "ensure", Account: placement.Account}); err != nil {
		return false, fmt.Errorf("create hosting account %s: %w", placement.Account, err)
	}
	recorded := false
	existing, err := sitelayout.Get(db, placement.Site)
	switch {
	case errors.Is(err, sitelayout.ErrNotPlaced):
		if err := sitelayout.Place(db, placement); err != nil {
			return false, err
		}
		recorded = true
	case err != nil:
		return false, err
	case existing != placement:
		return false, fmt.Errorf("site %s is already placed as %s %s in account %s", placement.Site, existing.Kind, existing.Directory, existing.Account)
	}
	if _, err := accountHelperContext(ctx, cfg, rootbroker.AccountRequest{
		Action: "place", Account: placement.Account, Site: placement.Site, Kind: string(placement.Kind), Directory: placement.Directory,
	}); err != nil {
		if recorded {
			if removeErr := sitelayout.Remove(db, placement.Site); removeErr != nil {
				return false, errors.Join(fmt.Errorf("publish site placement: %w", err), fmt.Errorf("forget unpublished placement: %w", removeErr))
			}
		}
		return false, fmt.Errorf("publish site placement: %w", err)
	}
	return recorded, nil
}

// unplaceSite forgets a site's placement after its tree and helper state are
// gone. The account and its Unix user stay; they may hold other sites.
func unplaceSite(ctx context.Context, cfg Config, db *sql.DB, site string) error {
	placement, err := sitelayout.Get(db, site)
	if errors.Is(err, sitelayout.ErrNotPlaced) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := accountHelperContext(ctx, cfg, rootbroker.AccountRequest{Action: "unplace", Account: placement.Account, Site: site}); err != nil {
		return fmt.Errorf("remove site placement on the host: %w", err)
	}
	return sitelayout.Remove(db, site)
}
