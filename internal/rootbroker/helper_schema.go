package rootbroker

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Fixed helper command arguments are validated after being constructed from a
// typed broker request. This table is not an RPC surface: clients cannot send
// helper names, actions, or positional argument arrays.

// helperArg validates one positional argument. args holds the full argument
// list so path checks can bind to the site named elsewhere in the request.
type helperArg func(v *Validator, value string, args []string) error

type fixedCommandAction struct {
	args     []helperArg
	optional []helperArg // trailing arguments that may be omitted, in order
}

var (
	schemaSitePattern       = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	schemaSessionPattern    = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	schemaProxyNamePattern  = regexp.MustCompile(`^[a-z0-9_-]{1,32}-[a-z0-9_-]+\.conf$`)
	schemaRouteNamePattern  = regexp.MustCompile(`^site-[a-z0-9_-]{1,32}-[a-z0-9_-]+\.conf$`)
	schemaAuthUserPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	schemaBcryptPattern     = regexp.MustCompile(`^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)
	schemaPythonVersion     = regexp.MustCompile(`^3\.(12|13)$`)
	schemaEntrypointPattern = regexp.MustCompile(`^[A-Za-z0-9_./:-]{1,160}$`)
	schemaImagePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,180}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[0-9a-f]{64}$`)
	schemaReleasePattern    = regexp.MustCompile(`^\.stepanel-release-[A-Za-z0-9-]+$`)
	schemaRunnerScript      = regexp.MustCompile(`^(runner|pipeline)-[0-9]+\.sh$`)
)

func matches(pattern *regexp.Regexp, what string) helperArg {
	return func(_ *Validator, value string, _ []string) error {
		if !pattern.MatchString(value) {
			return fmt.Errorf("invalid %s", what)
		}
		return nil
	}
}

func oneOf(what string, allowed ...string) helperArg {
	return func(_ *Validator, value string, _ []string) error {
		for _, candidate := range allowed {
			if value == candidate {
				return nil
			}
		}
		return fmt.Errorf("invalid %s", what)
	}
}

func intRange(what string, min, max int64) helperArg {
	return func(_ *Validator, value string, _ []string) error {
		if value == "" || len(value) > 19 || strings.TrimLeft(value, "0123456789") != "" {
			return fmt.Errorf("invalid %s", what)
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < min || n > max {
			return fmt.Errorf("%s out of range", what)
		}
		return nil
	}
}

var (
	argSite    = matches(schemaSitePattern, "site")
	argAccount = matches(schemaSitePattern, "account")
	// argOptionalAccount accepts an empty account, which the helper treats as
	// "no account slice".
	argOptionalAccount = func(v *Validator, value string, args []string) error {
		if value == "" {
			return nil
		}
		return argAccount(v, value, args)
	}
	argCount = intRange("limit", 0, 1<<40)
)

// argSitePath requires a clean absolute path equal to {webRoot}/sites/{site}
// joined with suffix, where site is the argument at sitePos.
func argSitePath(sitePos int, suffix string) helperArg {
	return func(v *Validator, value string, args []string) error {
		want := filepath.Join(v.webRoot, "sites", args[sitePos], suffix)
		if value != want {
			return fmt.Errorf("path must be the site's %s directory", suffix)
		}
		return nil
	}
}

// argReleasePath requires {webRoot}/sites/{site}/.stepanel-release-<id>.
func argReleasePath(sitePos int) helperArg {
	return func(v *Validator, value string, args []string) error {
		siteRoot := filepath.Join(v.webRoot, "sites", args[sitePos])
		if filepath.Clean(value) != value || filepath.Dir(value) != siteRoot || !schemaReleasePattern.MatchString(filepath.Base(value)) {
			return fmt.Errorf("path must be a release staging directory of the site")
		}
		return nil
	}
}

// argRunnerRoot accepts the site's public directory or a release staging
// directory of the same site.
func argRunnerRoot(sitePos int) helperArg {
	public, release := argSitePath(sitePos, "public"), argReleasePath(sitePos)
	return func(v *Validator, value string, args []string) error {
		if public(v, value, args) == nil || release(v, value, args) == nil {
			return nil
		}
		return fmt.Errorf("invalid build root")
	}
}

func argRunnerScript(_ *Validator, value string, _ []string) error {
	// Pipeline scripts are created by the unprivileged panel process in this
	// fixed, root-readable application directory.  Restricting the complete
	// path matters: checking only the basename would let a caller make the
	// root helper copy an unrelated readable file (for example
	// /etc/runner-1.sh) into a customer's build context.
	const appRoot = "/var/lib/ste-panel/apps"
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || filepath.Dir(value) != appRoot || !schemaRunnerScript.MatchString(filepath.Base(value)) {
		return fmt.Errorf("invalid build script path")
	}
	return nil
}

// argProxyBackend mirrors stepanel-proxyctl valid_backend: loopback or a
// private address with a port, never a metadata endpoint.
func resourceLimits() []helperArg {
	return []helperArg{argCount, argCount, argCount, argCount, argCount, argCount}
}

func withArgs(prefix []helperArg, rest ...helperArg) []helperArg {
	return append(append([]helperArg{}, prefix...), rest...)
}

var fixedCommandSchemas = map[string]map[string]fixedCommandAction{
	"appctl": {
		"resource-apply":         {args: withArgs([]helperArg{argSite}, resourceLimits()...), optional: []helperArg{argOptionalAccount}},
		"account-resource-apply": {args: withArgs([]helperArg{argAccount}, resourceLimits()...)},
		"resource-status":        {args: []helperArg{argSite}},
	},
	"runnerctl": {"build": {args: []helperArg{
		argSite, matches(schemaImagePattern, "container image"), argRunnerRoot(0), argRunnerScript,
		intRange("CPU percent", 25, 6400), intRange("memory MB", 64, 1048576), intRange("tasks max", 16, 100000),
		oneOf("network mode", "none", "egress"), intRange("max image bytes", 1, 1<<50),
	}}},
}

// validateFixedCommandArgs checks the argv constructed by one typed request.
func (v *Validator) validateFixedCommandArgs(name, action string, args []string) error {
	spec, ok := fixedCommandSchemas[name][action]
	if !ok {
		return fmt.Errorf("helper action is not allow-listed: %s/%s", name, action)
	}
	min, max := len(spec.args), len(spec.args)+len(spec.optional)
	if len(args) < min || len(args) > max {
		return fmt.Errorf("helper %s/%s takes %d argument(s), got %d", name, action, min, len(args))
	}
	for i, arg := range args {
		if len(arg) > 4096 || strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("invalid helper argument")
		}
		check := spec.args
		position := i
		if i >= len(spec.args) {
			check, position = spec.optional, i-len(spec.args)
		}
		if err := check[position](v, arg, args); err != nil {
			return fmt.Errorf("helper %s/%s argument %d: %w", name, action, i+1, err)
		}
	}
	return nil
}
