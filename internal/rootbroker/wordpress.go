package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"

	stepanelhelper "github.com/cyberducttape/StePanel/internal/helper"
)

const maxWordPressValue = 4096

var (
	wordPressThemePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	wordPressPluginPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}(/[A-Za-z0-9][A-Za-z0-9._-]{0,99})?\.php$`)
	wordPressDBNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	wordPressDBHostPattern   = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.:/-]{0,254}$`)
	wordPressDBPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
)

// wordPressReadOnlyActions do not change the site and need no fencing token.
var wordPressReadOnlyActions = map[string]bool{"status": true, "maintenance-status": true, "option-get": true}

// WordPressArgs returns the wp-cli arguments (after --path) for a validated
// request. It is the single argv builder for the broker and for development
// hosts without one, so both paths run identical commands.
func WordPressArgs(req *WordPressRequest) ([]string, error) {
	if req == nil {
		return nil, errors.New("wordpress request is nil")
	}
	// Panel-driven maintenance runs without plugins or themes so it does not
	// depend on, or execute, extension code. Customer-requested updates and
	// cron need the full site, as they did before.
	skip := []string{"--skip-plugins", "--skip-themes"}
	switch req.Action {
	case "status":
		return []string{"core", "version", "--format=json"}, nil
	case "core-update":
		return []string{"core", "update"}, nil
	case "plugin-update-all":
		return []string{"plugin", "update", "--all"}, nil
	case "theme-update-all":
		return []string{"theme", "update", "--all"}, nil
	case "cron-run-due":
		return []string{"cron", "event", "run", "--due-now"}, nil
	case "maintenance-activate":
		return append(skip, "maintenance-mode", "activate"), nil
	case "maintenance-deactivate":
		return append(skip, "maintenance-mode", "deactivate"), nil
	case "maintenance-status":
		return append(skip, "maintenance-mode", "is-active"), nil
	case "option-get":
		if req.Name != "siteurl" && req.Name != "home" {
			return nil, errors.New("unsupported WordPress option")
		}
		return append(skip, "option", "get", req.Name), nil
	case "option-update":
		switch req.Name {
		case "home", "siteurl":
			if err := validateWordPressURL(req.Value); err != nil {
				return nil, err
			}
		case "template", "stylesheet":
			if !wordPressThemePattern.MatchString(req.Value) {
				return nil, errors.New("invalid WordPress theme")
			}
		case "active_plugins":
			var plugins []string
			if len(req.Value) > maxWordPressValue || json.Unmarshal([]byte(req.Value), &plugins) != nil {
				return nil, errors.New("active plugins must be a JSON array of strings")
			}
			for _, plugin := range plugins {
				if !wordPressPluginPattern.MatchString(plugin) || strings.Contains(plugin, "..") {
					return nil, errors.New("invalid WordPress plugin path")
				}
			}
			return append(skip, "option", "update", req.Name, req.Value, "--format=json"), nil
		default:
			return nil, errors.New("unsupported WordPress option")
		}
		return append(skip, "option", "update", req.Name, req.Value), nil
	case "search-replace":
		if err := validateWordPressURL(req.Search); err != nil {
			return nil, err
		}
		if err := validateWordPressURL(req.Replace); err != nil {
			return nil, err
		}
		return append(skip, "search-replace", req.Search, req.Replace, "--all-tables-with-prefix", "--precise", "--recurse-objects", "--skip-columns=guid", "--quiet"), nil
	case "rewrite-flush":
		return append(skip, "rewrite", "flush"), nil
	case "cache-flush":
		return append(skip, "cache", "flush"), nil
	case "config-create":
		if !wordPressDBNamePattern.MatchString(req.DBName) || !wordPressDBNamePattern.MatchString(req.DBUser) || len(req.DBUser) > 32 {
			return nil, errors.New("invalid WordPress database identity")
		}
		if !wordPressDBHostPattern.MatchString(req.DBHost) || !wordPressDBPrefixPattern.MatchString(req.DBPrefix) {
			return nil, errors.New("invalid WordPress database host or prefix")
		}
		if err := validateWordPressSecret(req.Secret); err != nil {
			return nil, err
		}
		return []string{"config", "create", "--dbname=" + req.DBName, "--dbuser=" + req.DBUser, "--dbhost=" + req.DBHost, "--dbprefix=" + req.DBPrefix, "--prompt=dbpass", "--skip-check", "--skip-salts"}, nil
	case "config-set":
		switch req.Name {
		case "DB_NAME", "DB_USER":
			if !wordPressDBNamePattern.MatchString(req.Value) {
				return nil, errors.New("invalid WordPress database identity")
			}
		case "DB_HOST":
			if !wordPressDBHostPattern.MatchString(req.Value) {
				return nil, errors.New("invalid WordPress database host")
			}
		default:
			return nil, errors.New("unsupported WordPress configuration constant")
		}
		return []string{"config", "set", req.Name, req.Value, "--type=constant"}, nil
	case "config-set-password":
		if err := validateWordPressSecret(req.Secret); err != nil {
			return nil, err
		}
		return []string{"config", "set", "DB_PASSWORD", "--type=constant", "--prompt=value"}, nil
	default:
		return nil, errors.New("unsupported WordPress action")
	}
}

func validateWordPressURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || len(value) > maxWordPressValue || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("WordPress URL must be an http or https URL")
	}
	return nil
}

func validateWordPressSecret(secret string) error {
	if secret == "" || len(secret) > 256 || strings.ContainsAny(secret, "\x00\r\n") {
		return errors.New("invalid WordPress database password")
	}
	return nil
}

func (v *Validator) validateWordPressRequest(req *WordPressRequest) error {
	if req == nil {
		return errors.New("wordpress request is nil")
	}
	if err := v.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}
	_, err := WordPressArgs(req)
	return err
}

// WordPressMaintenanceActive interprets "maintenance-mode is-active", which
// reports only through its exit status: 0 active, 1 inactive. Output is not
// a signal (PHP notices print to stdout). A stepanel-appctl refusal also
// exits 1; reading it as "inactive" is safe because the activation that
// follows is refused the same way and the backup stays crash-consistent.
func WordPressMaintenanceActive(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// handleWordPressRequest runs wp-cli through stepanel-appctl, which fixes the
// executable and site path and drops to the site's isolated user, so site
// PHP never runs with panel or root privileges.
func (b *Broker) handleWordPressRequest(ctx context.Context, req *WordPressRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "wordpress request is nil"}, nil
	}
	args, err := WordPressArgs(req)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	switch req.Action {
	case "status", "core-update", "plugin-update-all", "theme-update-all", "cron-run-due", "maintenance-activate", "maintenance-deactivate", "maintenance-status", "option-get", "option-update", "search-replace", "rewrite-flush", "cache-flush", "config-create", "config-set", "config-set-password":
	default:
		return &Response{OK: false, Error: "unsupported WordPress action"}, nil
	}
	cmd := stepanelhelper.NewCommand(ctx, b.appctlPath, append([]string{"wp", req.Site}, args...)...)
	cmd.Stdin = strings.NewReader(req.Secret + "\n")
	stdout, stderr, runErr := stepanelhelper.RunCappedSeparate(ctx, cmd, maxBrokerCommandOutput, maxBrokerCommandOutput)
	result := WordPressResponse{Output: string(stdout)}
	if req.Action == "maintenance-status" {
		active, statusErr := WordPressMaintenanceActive(runErr)
		if statusErr != nil {
			return &Response{OK: false, Error: fmt.Sprintf("WordPress maintenance-status failed: %v: %s", statusErr, strings.TrimSpace(string(stderr)))}, nil
		}
		result.Active = active
	} else if runErr != nil {
		return &Response{OK: false, Error: fmt.Sprintf("WordPress %s failed: %v: %s", req.Action, runErr, strings.TrimSpace(string(stderr)))}, nil
	}
	details, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}
