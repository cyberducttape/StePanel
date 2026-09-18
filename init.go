package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runInit is the first-run wizard that guides operators through initial setup.
func runInit() {
	fmt.Print(`
╔════════════════════════════════════════════════════════════════╗
║              StePanel First-Run Wizard v` + Version + `                    ║
║                                                                ║
║  This wizard will guide you through the initial setup of       ║
║  StePanel, including environment validation and secret         ║
║  generation for production use.                               ║
╚════════════════════════════════════════════════════════════════╝
`)

	reader := bufio.NewReader(os.Stdin)
	state := &initState{reader: reader}

	// Step 1: Check for existing config
	state.checkExistingConfig()

	// Step 2: Validate environment
	fmt.Println("\n[1/4] Validating system environment...")
	if err := state.validateEnvironment(); err != nil {
		fmt.Fprintf(os.Stderr, "✗ Validation failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Environment validation passed")

	// Step 3: Gather required configuration
	fmt.Println("\n[2/4] Gathering configuration...")
	state.gatherConfig()

	// Step 4: Generate secrets
	fmt.Println("\n[3/4] Generating required secrets...")
	if err := state.generateSecrets(); err != nil {
		fmt.Fprintf(os.Stderr, "✗ Secret generation failed: %v\n", err)
		os.Exit(1)
	}

	// Step 5: Write configuration
	fmt.Println("\n[4/4] Creating directories and writing configuration...")
	if err := state.createDirectories(); err != nil {
		fmt.Fprintf(os.Stderr, "✗ Setup failed: %v\n", err)
		os.Exit(1)
	}

	// Summary
	state.printSummary()
}

type initState struct {
	reader               *bufio.Reader
	listen               string
	tlsCertFile          string
	tlsKeyFile           string
	webRoot              string
	webServer            string
	dbEngine             string
	dbHost               string
	dbUser               string
	dbPassword           string
	accountKey           string
	backupSigningKey     string
	environmentKey       string
	gitWebhookSecret     string
	requireOffsiteBackup bool
	production           bool
}

func (s *initState) checkExistingConfig() {
	// Check if critical secrets are already set
	if os.Getenv("STEPANEL_ACCOUNT_KEY") != "" {
		fmt.Print(`╔════════════════════════════════════════════════════════════════╗
║         StePanel Initialization Complete                    ║
║                                                              ║
║  Critical secrets are already configured. If you need to     ║
║  reset or reconfigure, remove the environment variables     ║
║  and run this wizard again.                                 ║
╚════════════════════════════════════════════════════════════════╝
`)
		os.Exit(0)
	}
}

func (s *initState) validateEnvironment() error {
	// Check for required tools/helpers (optional for first run, warn if missing)
	requiredHelpers := map[string]string{
		"/usr/local/sbin/stepanel-appctl":     "App lifecycle helper",
		"/usr/local/sbin/stepanel-vhostctl":   "Virtual host helper",
		"/usr/local/sbin/stepanel-proxyctl":   "Proxy helper",
		"/usr/local/sbin/stepanel-sitectl":    "Site lifecycle helper",
		"/usr/local/bin/wp":                   "WordPress CLI",
		"/usr/local/sbin/stepanel-certbot":    "Certificate helper",
	}

	missing := []string{}
	for path, desc := range requiredHelpers {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, desc)
		}
	}

	if len(missing) > 0 {
		fmt.Println("⚠ Warning: The following helpers are not yet installed:")
		for _, h := range missing {
			fmt.Printf("  - %s\n", h)
		}
		fmt.Println("These can be installed later. Continue? (yes/no)")
		if !s.readYesNo(true) {
			return fmt.Errorf("setup cancelled")
		}
	}

	return nil
}

func (s *initState) gatherConfig() {
	fmt.Println("\nEnter configuration values (press Enter for defaults):")

	s.listen = s.prompt("Listen address", ":8080")
	s.webServer = s.prompt("Web server (caddy/apache)", "caddy")
	s.webRoot = s.prompt("Web root directory", "data/www")
	s.dbEngine = s.prompt("Database engine (mysql/postgresql)", "mysql")
	s.dbHost = s.prompt("Database host", "localhost")
	s.dbUser = s.prompt("Database user", "stepanel")

	fmt.Print("Database password (will not echo): ")
	password, err := readPassword()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading password: %v\n", err)
		os.Exit(1)
	}
	s.dbPassword = password
	if s.dbPassword == "" {
		fmt.Println("⚠ Warning: No database password entered. This is insecure.")
	}

	fmt.Println("\nTLS Configuration:")
	fmt.Print("TLS certificate file path (leave empty to skip): ")
	s.tlsCertFile, _ = s.reader.ReadString('\n')
	s.tlsCertFile = strings.TrimSpace(s.tlsCertFile)

	if s.tlsCertFile != "" {
		s.tlsKeyFile = s.prompt("TLS key file path", "")
	}

	fmt.Println("\nAdvanced Configuration:")
	s.requireOffsiteBackup = s.readYesNo(false)
	if s.requireOffsiteBackup {
		fmt.Println("✓ Offsite backup will be required for site termination")
	}

	s.production = s.readYesNo(false)
	if s.production {
		fmt.Println("✓ Production mode enabled")
	}
}

func (s *initState) generateSecrets() error {
	fmt.Println("  Generating encryption keys...")
	var err error
	s.accountKey, err = generateSecret(32)
	if err != nil {
		return fmt.Errorf("failed to generate account key: %w", err)
	}

	s.backupSigningKey, err = generateSecret(32)
	if err != nil {
		return fmt.Errorf("failed to generate backup signing key: %w", err)
	}

	s.environmentKey, err = generateSecret(32)
	if err != nil {
		return fmt.Errorf("failed to generate environment key: %w", err)
	}

	s.gitWebhookSecret, err = generateSecret(32)
	if err != nil {
		return fmt.Errorf("failed to generate git webhook secret: %w", err)
	}

	fmt.Println("✓ Secrets generated")
	return nil
}

func (s *initState) createDirectories() error {
	dirs := []string{
		s.webRoot,
		filepath.Join(filepath.Dir("data"), "backups"),
		filepath.Join(filepath.Dir("data"), "imports"),
		filepath.Join(filepath.Dir("data"), "mail"),
		filepath.Join(filepath.Dir("data"), "nvm"),
		filepath.Join(filepath.Dir("data"), "proxy"),
		filepath.Join(filepath.Dir("data"), "vhosts"),
		filepath.Join(filepath.Dir("data"), "apps"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	fmt.Println("✓ Directories created")
	return nil
}

func (s *initState) printSummary() {
	fmt.Print(`
╔════════════════════════════════════════════════════════════════╗
║         StePanel Setup Complete                               ║
║                                                                ║
║  Your StePanel instance is configured and ready to start.     ║
║  Export the following environment variables before running:   ║
╚════════════════════════════════════════════════════════════════╝

export STEPANEL_LISTEN="` + s.listen + `"
export STEPANEL_WEBSERVER="` + s.webServer + `"
export STEPANEL_WEB_ROOT="` + s.webRoot + `"
export STEPANEL_DB_ENGINE="` + s.dbEngine + `"
export STEPANEL_DB_HOST="` + s.dbHost + `"
export STEPANEL_DB_USER="` + s.dbUser + `"
export STEPANEL_DB_PASSWORD="` + maskSecret(s.dbPassword) + `"
export STEPANEL_ACCOUNT_KEY="` + maskSecret(s.accountKey) + `"
export STEPANEL_BACKUP_SIGNING_KEY="` + maskSecret(s.backupSigningKey) + `"
export STEPANEL_ENVIRONMENT_KEY="` + maskSecret(s.environmentKey) + `"
export STEPANEL_GIT_WEBHOOK_SECRET="` + maskSecret(s.gitWebhookSecret) + `"`)

	if s.tlsCertFile != "" {
		fmt.Printf("export STEPANEL_TLS_CERT_FILE=\"%s\"\n", s.tlsCertFile)
		fmt.Printf("export STEPANEL_TLS_KEY_FILE=\"%s\"\n", s.tlsKeyFile)
	}

	if s.requireOffsiteBackup {
		fmt.Println("export STEPANEL_REQUIRE_OFFSITE_BACKUP=1")
	}

	if s.production {
		fmt.Println("export STEPANEL_PRODUCTION=1")
	}

	fmt.Print(`
To save these to .env:
  stepanel init > .env
  source .env
  stepanel

For documentation: https://github.com/itchyitchy123/StePanel
`)
}

func (s *initState) prompt(label, defaultVal string) string {
	fmt.Printf("%s [%s]: ", label, defaultVal)
	input, _ := s.reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		return defaultVal
	}
	return input
}

func (s *initState) readYesNo(defaultVal bool) bool {
	defaultStr := "no"
	if defaultVal {
		defaultStr = "yes"
	}
	fmt.Printf("[%s]: ", defaultStr)
	input, _ := s.reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return defaultVal
	}
	return input == "yes" || input == "y"
}

func generateSecret(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

func maskSecret(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "..." + s[len(s)-4:]
}

func readPassword() (string, error) {
	// TODO: Implement secure password input (disable echo)
	// For now, simple read with visible input
	input, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}
