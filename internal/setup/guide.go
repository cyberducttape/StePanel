// Package setup implements the guided first-time setup. It asks the
// operator a short series of questions, validates every answer immediately
// (including proving the authenticator app and the offsite backup remote
// work), generates all keys, and produces the settings install.sh needs.
//
// The guide performs no installation itself and never prints secrets. Side
// effects that touch the host (hashing, clock, rclone) go through Host so the
// flow is testable with scripted input.
package setup

import (
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
)

// ErrCancelled is returned when the operator declines to continue or input
// ends before the guide finishes.
var ErrCancelled = errors.New("setup cancelled; nothing was written")

// Host provides the guide's side effects.
type Host interface {
	// HashPassword returns the stored form of the administrator password.
	HashPassword(password string) (string, error)
	// VerifyTOTP reports whether code is valid for the base32 secret at now,
	// allowing one 30-second step of clock drift.
	VerifyTOTP(secret, code string, now time.Time) bool
	Now() time.Time
	// Hostname suggests a default panel hostname; it may be empty.
	Hostname() string
	// Rclone reports whether rclone is installed.
	Rclone() bool
	RcloneRemotes() ([]string, error)
	// RcloneConfigFile returns the absolute path of the rclone config in use.
	RcloneConfigFile() (string, error)
	// ProbeOffsite writes, reads back, and deletes a small object at target.
	ProbeOffsite(target string) error
	// SSHClientIP suggests a trusted management address; it may be empty.
	SSHClientIP() string
	Random(n int) ([]byte, error)
}

// Terminal reads operator input.
type Terminal interface {
	ReadLine() (string, error)
	// ReadSecret reads input without echoing it.
	ReadSecret() (string, error)
}

// Answers is the complete result of the guide.
type Answers struct {
	PanelHostname     string
	WebServer         string
	InstallTLS        bool
	DBEngine          string
	AdminUsername     string
	AdminPasswordHash string
	TOTPSecret        string
	OffsiteTarget     string
	RcloneConfig      string
	Fail2ban          bool
	Fail2banIgnoreIP  string
	InstallNode       bool
	InstallSecurity   bool
	InstallDBAdmin    bool
	// Keys maps installer variable names to generated key material.
	Keys map[string]string
}

// generatedKeys are created fresh for every new installation. They must
// never be regenerated for an existing one: the backup keys are required to
// restore existing backups and the audit key anchors the audit chain.
var generatedKeys = []string{
	"STEPANEL_SESSION_SECRET",
	"STEPANEL_AUDIT_KEY",
	"STEPANEL_ACCOUNT_KEY",
	"STEPANEL_ENVIRONMENT_KEY",
	"STEPANEL_BACKUP_SIGNING_KEY",
	"STEPANEL_BACKUP_ENCRYPTION_KEY",
}

// Env returns the installer settings in a stable order.
func (a Answers) Env() [][2]string {
	flag := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	env := [][2]string{
		{"STEPANEL_PANEL_HOSTNAME", a.PanelHostname},
		{"STEPANEL_WEBSERVER", a.WebServer},
		{"STEPANEL_INSTALL_TLS", flag(a.InstallTLS)},
		{"STEPANEL_DB_ENGINE", a.DBEngine},
		{"STEPANEL_DB_VERSION", "default"},
		{"STEPANEL_ADMIN_USERNAME", a.AdminUsername},
		{"STEPANEL_ADMIN_PASSWORD_HASH", a.AdminPasswordHash},
		{"STEPANEL_ADMIN_TOTP_SECRET", a.TOTPSecret},
		{"STEPANEL_REQUIRE_OFFSITE_BACKUP", "1"},
		{"STEPANEL_OFFSITE_TARGET", a.OffsiteTarget},
	}
	if a.RcloneConfig != "" {
		env = append(env, [2]string{"RCLONE_CONFIG", a.RcloneConfig})
	}
	env = append(env,
		[2]string{"STEPANEL_INSTALL_FAIL2BAN", flag(a.Fail2ban)},
		[2]string{"STEPANEL_INSTALL_NODE", flag(a.InstallNode)},
		[2]string{"STEPANEL_INSTALL_SECURITY", flag(a.InstallSecurity)},
		[2]string{"STEPANEL_INSTALL_DB_ADMIN", flag(a.InstallDBAdmin)},
	)
	if a.Fail2ban {
		env = append(env, [2]string{"STEPANEL_FAIL2BAN_IGNORE_IP", a.Fail2banIgnoreIP})
	}
	for _, name := range generatedKeys {
		env = append(env, [2]string{name, a.Keys[name]})
	}
	return env
}

var envKeyPattern = regexp.MustCompile(`^(STEPANEL_[A-Z0-9_]+|RCLONE_CONFIG)$`)

// EncodeEnvFile renders settings in the format install.sh reads:
// KEY="value", escaping only backslash and double quote.
func EncodeEnvFile(env [][2]string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# StePanel installer settings written by `stepanel setup`.\n")
	b.WriteString("# Contains secrets: keep this file root-only and store a copy offline.\n")
	for _, pair := range env {
		key, value := pair[0], pair[1]
		if !envKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("unsupported setting name %q", key)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("setting %s contains a line break", key)
		}
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)
		fmt.Fprintf(&b, "%s=\"%s\"\n", key, value)
	}
	return []byte(b.String()), nil
}

// Guide runs the interactive setup.
type Guide struct {
	term Terminal
	out  io.Writer
	host Host
}

func NewGuide(term Terminal, out io.Writer, host Host) *Guide {
	return &Guide{term: term, out: out, host: host}
}

func (g *Guide) say(format string, args ...any) {
	fmt.Fprintf(g.out, format+"\n", args...)
}

func (g *Guide) step(number, total int, title string) {
	g.say("")
	g.say("Step %d of %d · %s", number, total, title)
	g.say("%s", strings.Repeat("─", 48))
}

func (g *Guide) line() (string, error) {
	value, err := g.term.ReadLine()
	if err != nil {
		return "", ErrCancelled
	}
	return strings.TrimSpace(value), nil
}

// ask prompts until validate accepts the answer.
func (g *Guide) ask(question, fallback string, validate func(string) error) (string, error) {
	for {
		if fallback != "" {
			fmt.Fprintf(g.out, "%s [%s]: ", question, fallback)
		} else {
			fmt.Fprintf(g.out, "%s: ", question)
		}
		value, err := g.line()
		if err != nil {
			return "", err
		}
		if value == "" {
			value = fallback
		}
		if validate != nil {
			if err := validate(value); err != nil {
				g.say("  ✗ %v", err)
				continue
			}
		}
		return value, nil
	}
}

func (g *Guide) confirm(question string, fallback bool) (bool, error) {
	hint := "y/N"
	if fallback {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(g.out, "%s [%s]: ", question, hint)
		value, err := g.line()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(value) {
		case "":
			return fallback, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		g.say("  ✗ Please answer y or n.")
	}
}

type choice struct {
	key, label, detail string
}

func (g *Guide) choose(question string, options []choice, fallback string) (string, error) {
	for i, option := range options {
		marker := " "
		if option.key == fallback {
			marker = "*"
		}
		g.say("  %s %d) %-14s %s", marker, i+1, option.label, option.detail)
	}
	return g.ask(question, fallback, func(value string) error {
		for i, option := range options {
			if value == option.key || value == fmt.Sprint(i+1) || strings.EqualFold(value, option.label) {
				return nil
			}
		}
		return errors.New("choose one of the numbers or names listed")
	})
}

func resolveChoice(value string, options []choice) string {
	for i, option := range options {
		if value == option.key || value == fmt.Sprint(i+1) || strings.EqualFold(value, option.label) {
			return option.key
		}
	}
	return value
}

var adminUsernamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

const totalSteps = 7

// Run walks the operator through setup and returns the answers. Nothing is
// written; the caller decides where the result goes.
func (g *Guide) Run() (Answers, error) {
	answers := Answers{Keys: map[string]string{}}
	g.say("StePanel guided setup")
	g.say("")
	g.say("This asks %d short questions, checks each answer as you go, and", totalSteps)
	g.say("generates every key for you. Press Enter to accept a [default].")
	g.say("Nothing on this server changes until you confirm the installation.")

	if err := g.panelAddress(&answers); err != nil {
		return Answers{}, err
	}
	if err := g.webServer(&answers); err != nil {
		return Answers{}, err
	}
	if err := g.database(&answers); err != nil {
		return Answers{}, err
	}
	if err := g.administrator(&answers); err != nil {
		return Answers{}, err
	}
	if err := g.twoFactor(&answers); err != nil {
		return Answers{}, err
	}
	if err := g.offsite(&answers); err != nil {
		return Answers{}, err
	}
	if err := g.extras(&answers); err != nil {
		return Answers{}, err
	}
	for _, name := range generatedKeys {
		raw, err := g.host.Random(32)
		if err != nil {
			return Answers{}, fmt.Errorf("generate %s: %w", name, err)
		}
		answers.Keys[name] = hex.EncodeToString(raw)
	}
	g.review(answers)
	ok, err := g.confirm("Save these settings?", true)
	if err != nil {
		return Answers{}, err
	}
	if !ok {
		return Answers{}, ErrCancelled
	}
	return answers, nil
}

func (g *Guide) panelAddress(a *Answers) error {
	g.step(1, totalSteps, "Panel address")
	g.say("The name you will open in a browser, for example panel.yourcompany.com.")
	g.say("Its DNS record must point at this server so HTTPS can be set up.")
	fallback := strings.ToLower(g.host.Hostname())
	if !domainname.Valid(fallback) {
		fallback = ""
	}
	value, err := g.ask("Panel hostname", fallback, func(value string) error {
		if err := domainname.Validate(value); err != nil {
			return fmt.Errorf("%v (use a full name like panel.example.org)", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.PanelHostname = strings.ToLower(value)
	return nil
}

var webServers = []choice{
	{"caddy", "Caddy", "recommended · automatic HTTPS"},
	{"apache", "Apache", "widest .htaccess compatibility"},
	{"openlitespeed", "OpenLiteSpeed", "LiteSpeed cache · configure TLS yourself"},
}

func (g *Guide) webServer(a *Answers) error {
	g.step(2, totalSteps, "Web server")
	value, err := g.choose("Web server", webServers, "caddy")
	if err != nil {
		return err
	}
	a.WebServer = resolveChoice(value, webServers)
	switch a.WebServer {
	case "apache":
		a.InstallTLS, err = g.confirm("Get free Let's Encrypt certificates for sites automatically?", true)
		if err != nil {
			return err
		}
	case "openlitespeed":
		g.say("  ℹ OpenLiteSpeed TLS is configured outside StePanel.")
	default:
		g.say("  ✓ Caddy obtains and renews HTTPS certificates automatically.")
	}
	return nil
}

var databases = []choice{
	{"mariadb", "MariaDB", "recommended · MySQL-compatible"},
	{"mysql", "MySQL", ""},
	{"postgresql", "PostgreSQL", "cPanel MySQL dumps need conversion"},
}

func (g *Guide) database(a *Answers) error {
	g.step(3, totalSteps, "Database server")
	value, err := g.choose("Database", databases, "mariadb")
	if err != nil {
		return err
	}
	a.DBEngine = resolveChoice(value, databases)
	return nil
}

func (g *Guide) administrator(a *Answers) error {
	g.step(4, totalSteps, "Administrator account")
	username, err := g.ask("Administrator username", "admin", func(value string) error {
		if !adminUsernamePattern.MatchString(value) {
			return errors.New("use 1-64 letters, digits, dots, dashes, or underscores")
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.AdminUsername = username
	for {
		fmt.Fprint(g.out, "Password (at least 12 characters, 16+ recommended): ")
		password, err := g.term.ReadSecret()
		fmt.Fprintln(g.out)
		if err != nil {
			return ErrCancelled
		}
		if len(password) < 12 || len(password) > 1024 {
			g.say("  ✗ The password must be 12 to 1024 characters.")
			continue
		}
		if strings.ContainsAny(password, "\r\n") {
			g.say("  ✗ The password cannot contain line breaks.")
			continue
		}
		fmt.Fprint(g.out, "Repeat the password: ")
		repeat, err := g.term.ReadSecret()
		fmt.Fprintln(g.out)
		if err != nil {
			return ErrCancelled
		}
		if repeat != password {
			g.say("  ✗ The passwords do not match. Try again.")
			continue
		}
		hash, err := g.host.HashPassword(password)
		if err != nil {
			return fmt.Errorf("hash administrator password: %w", err)
		}
		a.AdminPasswordHash = hash
		g.say("  ✓ Password set. Only its hash is saved.")
		return nil
	}
}

// totpSecretBytes gives a 160-bit secret, the RFC 4226 recommendation and
// the installer's minimum.
const totpSecretBytes = 20

func (g *Guide) twoFactor(a *Answers) error {
	g.step(5, totalSteps, "Two-factor sign-in")
	raw, err := g.host.Random(totpSecretBytes)
	if err != nil {
		return fmt.Errorf("generate authenticator secret: %w", err)
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	var grouped []string
	for i := 0; i < len(secret); i += 4 {
		grouped = append(grouped, secret[i:min(i+4, len(secret))])
	}
	uri := (&url.URL{Scheme: "otpauth", Host: "totp", Path: "/StePanel:" + a.AdminUsername + "@" + a.PanelHostname,
		RawQuery: url.Values{"secret": {secret}, "issuer": {"StePanel"}}.Encode()}).String()
	g.say("Administrators sign in with a password and a 6-digit code from an")
	g.say("authenticator app (Google Authenticator, 1Password, Bitwarden, Authy, ...).")
	g.say("")
	g.say("In the app, choose \"Enter a setup key\" and type:")
	g.say("")
	g.say("    %s", strings.Join(grouped, " "))
	g.say("")
	g.say("Or paste this link into an app that accepts otpauth links:")
	g.say("    %s", uri)
	g.say("")
	for attempt := 1; ; attempt++ {
		code, err := g.ask("Enter the 6-digit code the app shows now", "", func(value string) error {
			value = strings.ReplaceAll(value, " ", "")
			if len(value) != 6 || strings.Trim(value, "0123456789") != "" {
				return errors.New("enter the 6 digits shown in the app")
			}
			return nil
		})
		if err != nil {
			return err
		}
		if g.host.VerifyTOTP(secret, strings.ReplaceAll(code, " ", ""), g.host.Now()) {
			a.TOTPSecret = secret
			g.say("  ✓ Authenticator confirmed. You will need it to sign in.")
			return nil
		}
		g.say("  ✗ That code does not match. Wait for the next code and try again.")
		if attempt%3 == 0 {
			g.say("  ℹ If codes keep failing, check this server's clock: %s UTC.", g.host.Now().UTC().Format("2006-01-02 15:04:05"))
			g.say("    It must be within 30 seconds of your phone (see `timedatectl`).")
		}
	}
}

func (g *Guide) offsite(a *Answers) error {
	g.step(6, totalSteps, "Offsite backups")
	g.say("StePanel requires a copy of every backup outside this server, so a")
	g.say("lost disk or server never means lost sites. It uses rclone, which")
	g.say("supports S3, Backblaze B2, Google Drive, SFTP, and many more.")
	if !g.host.Rclone() {
		g.say("")
		g.say("  ✗ rclone is not installed. To continue:")
		g.say("      1. Install it:  sudo apt install rclone   (or: sudo dnf install rclone)")
		g.say("      2. Add a storage remote:  sudo rclone config")
		g.say("      3. Run this setup again.")
		return fmt.Errorf("rclone is required for offsite backups: %w", ErrCancelled)
	}
	remotes, err := g.host.RcloneRemotes()
	if err != nil {
		return fmt.Errorf("list rclone remotes: %w", err)
	}
	if len(remotes) == 0 {
		g.say("")
		g.say("  ✗ rclone has no storage remotes yet. Add one with:  sudo rclone config")
		g.say("    then run this setup again.")
		return fmt.Errorf("no rclone remote is configured: %w", ErrCancelled)
	}
	g.say("")
	g.say("Configured rclone remotes: %s", strings.Join(remotes, ", "))
	fallback := strings.TrimSuffix(remotes[0], ":") + ":stepanel-backups"
	for {
		target, err := g.ask("Backup location (remote:path)", fallback, func(value string) error {
			name, path, ok := strings.Cut(value, ":")
			if !ok || name == "" || path == "" || strings.ContainsAny(value, " \t\"'\\") {
				return errors.New("use the form remote:path, for example " + fallback)
			}
			for _, remote := range remotes {
				if strings.TrimSuffix(remote, ":") == name {
					return nil
				}
			}
			return fmt.Errorf("%q is not one of the configured remotes", name)
		})
		if err != nil {
			return err
		}
		g.say("  … Testing %s (writes, reads back, and deletes a small file)", target)
		if err := g.host.ProbeOffsite(target); err != nil {
			g.say("  ✗ The test failed: %v", err)
			retry, err := g.confirm("Try a different location?", true)
			if err != nil {
				return err
			}
			if !retry {
				return fmt.Errorf("offsite backup location could not be verified: %w", ErrCancelled)
			}
			continue
		}
		g.say("  ✓ Backups can be written to and read from %s.", target)
		a.OffsiteTarget = target
		break
	}
	config, err := g.host.RcloneConfigFile()
	if err != nil {
		return fmt.Errorf("locate rclone configuration: %w", err)
	}
	a.RcloneConfig = config
	return nil
}

func (g *Guide) extras(a *Answers) error {
	g.step(7, totalSteps, "Optional features")
	want, err := g.confirm("Review optional features now? (you can add them later)", false)
	if err != nil || !want {
		return err
	}
	if a.Fail2ban, err = g.confirm("Block repeated failed sign-ins with Fail2ban?", true); err != nil {
		return err
	}
	if a.Fail2ban {
		g.say("  Fail2ban needs the addresses you manage this server from, so it")
		g.say("  never locks you out. Separate several with spaces.")
		a.Fail2banIgnoreIP, err = g.ask("Trusted IP addresses or networks", g.host.SSHClientIP(), func(value string) error {
			if value == "" {
				return errors.New("enter at least one address, for example 203.0.113.10 or 198.51.100.0/24")
			}
			for _, field := range strings.Fields(value) {
				if _, err := netip.ParseAddr(field); err == nil {
					continue
				}
				if _, err := netip.ParsePrefix(field); err == nil {
					continue
				}
				return fmt.Errorf("%q is not an IP address or network", field)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if a.InstallNode, err = g.confirm("Install Node.js for Node applications?", false); err != nil {
		return err
	}
	if a.InstallSecurity, err = g.confirm("Install malware scanning (ClamAV)?", false); err != nil {
		return err
	}
	label := "phpMyAdmin"
	if a.DBEngine == "postgresql" {
		label = "phpPgAdmin"
	}
	if a.InstallDBAdmin, err = g.confirm(fmt.Sprintf("Install the %s database tool (local access only)?", label), false); err != nil {
		return err
	}
	return nil
}

func (g *Guide) review(a Answers) {
	yes := func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	}
	g.say("")
	g.say("Review")
	g.say("%s", strings.Repeat("─", 48))
	g.say("  Panel address     https://%s", a.PanelHostname)
	g.say("  Web server        %s", a.WebServer)
	if a.WebServer == "apache" {
		g.say("  Site certificates %s", yes(a.InstallTLS))
	}
	g.say("  Database          %s", a.DBEngine)
	g.say("  Administrator     %s (password and authenticator confirmed)", a.AdminUsername)
	g.say("  Offsite backups   %s", a.OffsiteTarget)
	if a.Fail2ban {
		g.say("  Fail2ban          yes, trusting %s", a.Fail2banIgnoreIP)
	} else {
		g.say("  Fail2ban          no")
	}
	g.say("  Node.js           %s", yes(a.InstallNode))
	g.say("  Malware scanning  %s", yes(a.InstallSecurity))
	g.say("  Database tool     %s", yes(a.InstallDBAdmin))
	g.say("  Keys              %d generated (session, audit, account, environment, backup signing, backup encryption)", len(generatedKeys))
}
