package rootbroker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	stepanelhelper "github.com/cyberducttape/StePanel/internal/helper"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// --- Hosting-account operations (ADR-0007) ---

// AccountRequest manages a hosting account's Unix user and the placement of
// its sites. The broker cross-checks every request against the control-plane
// account and placement records before running stepanel-userctl, and the
// helper re-validates the user against the root-managed group.
type AccountRequest struct {
	Action    string `json:"action"` // ensure, delete, quota, quota-clear, place, unplace
	Account   string `json:"account"`
	Site      string `json:"site,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Directory string `json:"directory,omitempty"`
	DiskMB    int    `json:"disk_mb,omitempty"`
	Inodes    int    `json:"inodes,omitempty"`
}

// AccountResponse reports the account user and, for place, the document root.
type AccountResponse struct {
	UnixUser string `json:"unix_user,omitempty"`
	DocRoot  string `json:"doc_root,omitempty"`
}

const userctlPath = "/usr/local/sbin/stepanel-userctl"

func (v *Validator) validateAccountRequest(req *AccountRequest) error {
	if req == nil {
		return errors.New("account request is required")
	}
	if err := sitelayout.ValidUnixUser(req.Account); err != nil {
		return err
	}
	switch req.Action {
	case "ensure", "delete", "quota-clear":
		if req.Site != "" || req.Kind != "" || req.Directory != "" || req.DiskMB != 0 || req.Inodes != 0 {
			return fmt.Errorf("account %s accepts only an account", req.Action)
		}
	case "quota":
		if req.Site != "" || req.Kind != "" || req.Directory != "" {
			return errors.New("account quota accepts only an account and limits")
		}
		if req.DiskMB < 64 || req.DiskMB > 1048576 || req.Inodes < 1000 || req.Inodes > 1000000000 {
			return errors.New("account quota is out of range")
		}
	case "place":
		placement := sitelayout.Placement{Site: req.Site, Account: req.Account, UnixUser: req.Account, Kind: sitelayout.Kind(req.Kind), Directory: req.Directory}
		if err := placement.Validate(); err != nil {
			return err
		}
		if req.DiskMB != 0 || req.Inodes != 0 {
			return errors.New("site placement does not take quota limits")
		}
	case "unplace":
		if err := v.ValidateSiteName(req.Site); err != nil {
			return err
		}
		if req.Kind != "" || req.Directory != "" || req.DiskMB != 0 || req.Inodes != 0 {
			return errors.New("site unplacement accepts only an account and site")
		}
	default:
		return fmt.Errorf("unsupported account action %q", req.Action)
	}
	return nil
}

func (b *Broker) handleAccountRequest(ctx context.Context, req *AccountRequest) (*Response, error) {
	if b.fencingDB == nil {
		return &Response{OK: false, Error: "hosting-account operations require the control-plane database"}, nil
	}
	unixUser, active, err := sitelayout.AccountUser(b.fencingDB, req.Account)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	if req.Action != "delete" && !active {
		return &Response{OK: false, Error: fmt.Sprintf("account %q has no active Unix user in the control plane", req.Account)}, nil
	}
	if active && unixUser != req.Account {
		return &Response{OK: false, Error: fmt.Sprintf("account %q is recorded with Unix user %q", req.Account, unixUser)}, nil
	}
	var args []string
	switch req.Action {
	case "ensure":
		args = []string{"account-ensure", req.Account}
	case "delete":
		placements, err := sitelayout.ForAccount(b.fencingDB, req.Account)
		if err != nil {
			return &Response{OK: false, Error: err.Error()}, nil
		}
		if len(placements) > 0 {
			return &Response{OK: false, Error: fmt.Sprintf("account %q still has %d site(s)", req.Account, len(placements))}, nil
		}
		args = []string{"account-delete", req.Account}
	case "quota":
		args = []string{"account-quota", req.Account, strconv.Itoa(req.DiskMB), strconv.Itoa(req.Inodes)}
	case "quota-clear":
		args = []string{"account-quota-clear", req.Account}
	case "place":
		recorded, err := sitelayout.Get(b.fencingDB, req.Site)
		if err != nil {
			return &Response{OK: false, Error: fmt.Sprintf("site %q has no control-plane placement: %v", req.Site, err)}, nil
		}
		if recorded.Account != req.Account || recorded.UnixUser != req.Account || string(recorded.Kind) != req.Kind || recorded.Directory != req.Directory {
			return &Response{OK: false, Error: fmt.Sprintf("site %q placement request does not match the control plane", req.Site)}, nil
		}
		args = []string{"place", req.Site, req.Account, req.Kind, req.Directory}
	case "unplace":
		recorded, err := sitelayout.Get(b.fencingDB, req.Site)
		if err != nil {
			return &Response{OK: false, Error: fmt.Sprintf("site %q has no control-plane placement: %v", req.Site, err)}, nil
		}
		if recorded.Account != req.Account {
			return &Response{OK: false, Error: fmt.Sprintf("site %q is not placed in account %q", req.Site, req.Account)}, nil
		}
		args = []string{"unplace", req.Site, req.Account}
	}
	cmd := stepanelhelper.NewCommand(ctx, b.userctlPath(), args...)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("account %s failed: %v: %s", req.Action, err, strings.TrimSpace(string(output)))}, nil
	}
	details := AccountResponse{UnixUser: req.Account}
	if req.Action == "place" {
		if _, docRoot, found := strings.Cut(strings.TrimSpace(string(output)), "\t"); found {
			details.DocRoot = docRoot
		}
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: encoded}, nil
}

func (b *Broker) userctlPath() string {
	if b.userctl != "" {
		return b.userctl
	}
	return userctlPath
}

// placedSiteUser returns the account user of a site placed in a hosting
// account, or found=false for a legacy site. A control-plane database from
// before migration 21 has no placements table, which also means legacy.
func placedSiteUser(db *sql.DB, site string) (string, bool, error) {
	if db == nil {
		return "", false, nil
	}
	placement, err := sitelayout.Get(db, site)
	switch {
	case err == nil:
		return placement.UnixUser, true, nil
	case errors.Is(err, sitelayout.ErrNotPlaced), strings.Contains(err.Error(), "no such table"):
		return "", false, nil
	default:
		return "", false, err
	}
}
