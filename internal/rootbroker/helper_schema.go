package rootbroker

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Compatibility helper requests are validated against an explicit schema per
// helper action: exact arity and a semantic type for every positional
// argument. There is no free-form argument; an action that is not declared
// here cannot be forwarded to a root-owned helper, and a value that does not
// match its declared type is rejected before any process starts. The helper
// scripts keep their own checks as defence in depth, but the broker no longer
// relies on them as the only validation layer.

// helperArg validates one positional argument. args holds the full argument
// list so path checks can bind to the site named elsewhere in the request.
type helperArg func(v *Validator, value string, args []string) error

type helperAction struct {
	args     []helperArg
	optional []helperArg // trailing arguments that may be omitted, in order
}

var (
	schemaSitePattern       = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	schemaNamePattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	schemaDatabasePattern   = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	schemaDBUserPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	schemaEncodingPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	schemaSessionPattern    = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	schemaDomainPattern     = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	schemaProxyNamePattern  = regexp.MustCompile(`^[a-z0-9_-]{1,32}-[a-z0-9_-]+\.conf$`)
	schemaRouteNamePattern  = regexp.MustCompile(`^site-[a-z0-9_-]{1,32}-[a-z0-9_-]+\.conf$`)
	schemaAuthUserPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	schemaBcryptPattern     = regexp.MustCompile(`^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)
	schemaNodeVersion       = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	schemaPythonVersion     = regexp.MustCompile(`^3\.(12|13)$`)
	schemaPHPVersion        = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	schemaPHPSizePattern    = regexp.MustCompile(`^[1-9][0-9]{0,4}M$`)
	schemaPHPErrorPattern   = regexp.MustCompile(`^[A-Z0-9_~ &|]{1,80}$`)
	schemaEntrypointPattern = regexp.MustCompile(`^[A-Za-z0-9_./:-]{1,160}$`)
	schemaImagePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,180}@sha256:[0-9a-f]{64}$`)
	schemaGitRepoPattern    = regexp.MustCompile(`^git@[A-Za-z0-9.-]+:[A-Za-z0-9._/-]+\.git$`)
	schemaGitRefPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]{0,127}$`)
	schemaGitHostsPattern   = regexp.MustCompile(`^[A-Za-z0-9.,-]+$`)
	schemaReleasePattern    = regexp.MustCompile(`^\.stepanel-release-[A-Za-z0-9-]+$`)
	schemaRunnerScript      = regexp.MustCompile(`^(runner|pipeline)-[0-9]+\.sh$`)
	schemaIPv4Pattern       = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){3}$`)
	schemaPrivateIPv6       = regexp.MustCompile(`^\[([fF][cCdD][0-9A-Fa-f:]*|[fF][eE][89aAbB][0-9A-Fa-f:]*)\]$`)
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
	argSite     = matches(schemaSitePattern, "site")
	argAccount  = matches(schemaSitePattern, "account")
	argName     = matches(schemaNamePattern, "name")
	argDatabase = matches(schemaDatabasePattern, "database")
	argDBUser   = matches(schemaDBUserPattern, "database user")
	argFlag     = oneOf("flag", "0", "1")
	// argOptionalAccount accepts an empty account, which the helper treats as
	// "no account slice".
	argOptionalAccount = func(v *Validator, value string, args []string) error {
		if value == "" {
			return nil
		}
		return argAccount(v, value, args)
	}
	argCount  = intRange("limit", 0, 1<<40)
	argPort   = intRange("port", 1, 65535)
	argDomain = func(_ *Validator, value string, _ []string) error {
		if len(value) > 253 || !schemaDomainPattern.MatchString(value) {
			return fmt.Errorf("invalid domain")
		}
		return nil
	}
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
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || !schemaRunnerScript.MatchString(filepath.Base(value)) {
		return fmt.Errorf("invalid build script path")
	}
	return nil
}

// argProxyBackend mirrors stepanel-proxyctl valid_backend: loopback or a
// private address with a port, never a metadata endpoint.
func argProxyBackend(_ *Validator, value string, _ []string) error {
	i := strings.LastIndex(value, ":")
	if i <= 0 {
		return fmt.Errorf("invalid backend")
	}
	host, port := value[:i], value[i+1:]
	if argPort(nil, port, nil) != nil {
		return fmt.Errorf("invalid backend port")
	}
	if host == "localhost" || host == "[::1]" || schemaPrivateIPv6.MatchString(host) {
		return nil
	}
	if !schemaIPv4Pattern.MatchString(host) {
		return fmt.Errorf("invalid backend host")
	}
	var octets [4]int
	for k, part := range strings.Split(host, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n > 255 {
			return fmt.Errorf("invalid backend host")
		}
		octets[k] = n
	}
	switch {
	case octets[0] == 10, octets[0] == 127,
		octets[0] == 192 && octets[1] == 168,
		octets[0] == 172 && octets[1] >= 16 && octets[1] <= 31:
		return nil
	}
	return fmt.Errorf("backend must be a loopback or private address")
}

func resourceLimits() []helperArg {
	return []helperArg{argCount, argCount, argCount, argCount, argCount, argCount}
}

func withArgs(prefix []helperArg, rest ...helperArg) []helperArg {
	return append(append([]helperArg{}, prefix...), rest...)
}

var helperSchemas = map[string]map[string]helperAction{
	"appctl": {
		"python-apply":           {args: []helperArg{argSite, matches(schemaPythonVersion, "Python version"), argSitePath(0, "public"), matches(schemaEntrypointPattern, "entrypoint"), argPort, intRange("workers", 1, 64)}},
		"python-start":           {args: []helperArg{argSite}},
		"python-stop":            {args: []helperArg{argSite}},
		"python-restart":         {args: []helperArg{argSite}},
		"node-tool":              {args: []helperArg{argSite, oneOf("Node tool action", "install", "build"), oneOf("package manager", "npm", "yarn", "pnpm"), argSitePath(0, "public")}},
		"composer-install":       {args: []helperArg{argSite, argSitePath(0, "public"), argFlag, argFlag}},
		"env-apply":              {args: []helperArg{argSite}},
		"worker-apply":           {args: []helperArg{argSite, argName, oneOf("worker type", "laravel", "horizon", "node", "celery", "rq"), argSitePath(0, "public"), argCount, argCount, argCount}},
		"worker-delete":          {args: []helperArg{argSite, argName}},
		"worker-start":           {args: []helperArg{argSite, argName}},
		"worker-stop":            {args: []helperArg{argSite, argName}},
		"worker-restart":         {args: []helperArg{argSite, argName}},
		"resource-apply":         {args: withArgs([]helperArg{argSite}, resourceLimits()...), optional: []helperArg{argOptionalAccount}},
		"account-resource-apply": {args: withArgs([]helperArg{argAccount}, resourceLimits()...)},
		"resource-status":        {args: []helperArg{argSite}},
	},
	"proxyctl": {
		"apply":  {args: []helperArg{argSite, argDomain, argProxyBackend}},
		"delete": {args: []helperArg{matches(schemaProxyNamePattern, "proxy name")}},
		"reload": {},
	},
	"sitectl": {
		"prepare":      {args: []helperArg{argSite}},
		"prepare-root": {args: []helperArg{argSite}},
		"seal":         {args: []helperArg{argSite}},
		"delete":       {args: []helperArg{argSite}},
		"access":       {args: []helperArg{argSite, argFlag, argFlag}},
		"resources":    {args: []helperArg{argSite, intRange("PHP workers", 1, 512)}},
		"quota":        {args: []helperArg{argSite, argCount, argCount}},
		"quota-clear":  {args: []helperArg{argSite}},
		"runtime": {args: []helperArg{
			argSite, matches(schemaPHPVersion, "PHP version"),
			matches(schemaPHPSizePattern, "memory limit"), intRange("max execution time", 1, 99999),
			matches(schemaPHPSizePattern, "upload size"), matches(schemaPHPSizePattern, "post size"),
			intRange("max input vars", 1, 9999999), argFlag, argFlag,
			matches(schemaPHPErrorPattern, "error reporting"),
		}},
	},
	"vhostctl": {
		"apply":           {args: []helperArg{argSite, argDomain}},
		"apply-auth":      {args: []helperArg{argSite, argDomain, matches(schemaAuthUserPattern, "Basic Auth user"), matches(schemaBcryptPattern, "Basic Auth hash")}},
		"delete":          {args: []helperArg{matches(schemaRouteNamePattern, "site route name")}},
		"import-htaccess": {args: []helperArg{argSite, argDomain}},
	},
	"runnerctl": {
		"build": {args: []helperArg{
			argSite, matches(schemaImagePattern, "container image"), argRunnerRoot(0), argRunnerScript,
			intRange("CPU percent", 25, 6400), intRange("memory MB", 64, 1048576), intRange("tasks max", 16, 100000),
			oneOf("network mode", "none", "egress"), intRange("max image bytes", 1, 1<<50),
		}},
	},
	"gitctl": {
		"clone": {args: []helperArg{argSite, matches(schemaGitRepoPattern, "repository"), matches(schemaGitRefPattern, "ref"), argReleasePath(0), matches(schemaGitHostsPattern, "Git host allowlist")}},
	},
	"dbctl": {
		"reconcile":         {},
		"inventory":         {},
		"diagnostics":       {},
		"sessions":          {},
		"settings":          {},
		"list":              {args: []helperArg{argSite}},
		"terminate":         {args: []helperArg{matches(schemaSessionPattern, "session ID")}},
		"provision":         {args: []helperArg{argDatabase, argDBUser, argSite, matches(schemaEncodingPattern, "encoding")}},
		"rotate":            {args: []helperArg{argDatabase, argDBUser}},
		"drop-managed":      {args: []helperArg{argDatabase, argDBUser}},
		"restore":           {args: []helperArg{argDatabase, argSite}},
		"restore-dump":      {args: []helperArg{argDatabase, argSite}},
		"restore-wordpress": {args: []helperArg{argDatabase, argDBUser, argSite}},
		"cleanup-wordpress": {args: []helperArg{argDatabase, argDBUser}},
		"drop":              {args: []helperArg{argDatabase}},
		"dump":              {args: []helperArg{argDatabase}},
	},
}

// validateHelperArgs checks a compatibility helper request against its
// declared schema.
func (v *Validator) validateHelperArgs(name, action string, args []string) error {
	spec, ok := helperSchemas[name][action]
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
