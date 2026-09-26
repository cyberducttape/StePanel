package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// runInit is the first-run wizard that guides operators through initial setup.
func runInit() {
	outputFile := flag.String("output", "", "write configuration to file (e.g., /etc/stepanel.env)")
	flag.Parse()

	if *outputFile == "" {
		fmt.Fprintf(os.Stderr, "Usage: stepanel init --output /path/to/config.env\n")
		os.Exit(1)
	}
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

	// Step 1: Validate environment
	fmt.Println("\n[1/3] Validating system environment...")
	if err := state.validateEnvironment(); err != nil {
		fmt.Fprintf(os.Stderr, "✗ Validation failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Environment validation passed")

	// Step 2: Gather required configuration
	fmt.Println("\n[2/3] Gathering configuration and secrets...")
	state.gatherConfig()
	if err := state.generateSecrets(); err != nil {
		fmt.Fprintf(os.Stderr, "✗ Secret generation failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Secrets generated")

	// Step 3: Validate and write configuration
	fmt.Println("\n[3/3] Validating and writing configuration...")
	if err := state.writeAndValidateConfig(*outputFile); err != nil {
		fmt.Fprintf(os.Stderr, "✗ Configuration validation failed: %v\n", err)
		os.Exit(1)
	}
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

func (s *initState) validateEnvironment() error {
	// Check for required tools/helpers (optional for first run, warn if missing)
	requiredHelpers := map[string]string{
		"/usr/local/sbin/stepanel-appctl":   "App lifecycle helper",
		"/usr/local/sbin/stepanel-vhostctl": "Virtual host helper",
		"/usr/local/sbin/stepanel-proxyctl": "Proxy helper",
		"/usr/local/sbin/stepanel-sitectl":  "Site lifecycle helper",
		"/usr/local/bin/wp":                 "WordPress CLI",
		"/usr/local/sbin/stepanel-certbot":  "Certificate helper",
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

	fmt.Print("Database password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ Error reading password: %v\n", err)
		os.Exit(1)
	}
	fmt.Println()
	s.dbPassword = string(passwordBytes)
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
	fmt.Print("Require offsite backup before site termination? [no]: ")
	s.requireOffsiteBackup = s.readYesNo(false)
	if s.requireOffsiteBackup {
		fmt.Println("✓ Offsite backup will be required for site termination")
	}

	fmt.Print("Enable production mode? [no]: ")
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

func (s *initState) writeAndValidateConfig(outputPath string) error {
	// Helper to escape shell special characters in secret values
	shellEscape := func(value string) string {
		// Use single quotes which prevent all expansions, then escape any single quotes
		return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
	}

	lines := []string{
		fmt.Sprintf(`STEPANEL_LISTEN=%s`, shellEscape(s.listen)),
		fmt.Sprintf(`STEPANEL_WEBSERVER=%s`, shellEscape(s.webServer)),
		fmt.Sprintf(`STEPANEL_WEB_ROOT=%s`, shellEscape(s.webRoot)),
		fmt.Sprintf(`STEPANEL_DB_ENGINE=%s`, shellEscape(s.dbEngine)),
		fmt.Sprintf(`STEPANEL_DB_HOST=%s`, shellEscape(s.dbHost)),
		fmt.Sprintf(`STEPANEL_DB_USER=%s`, shellEscape(s.dbUser)),
		fmt.Sprintf(`STEPANEL_DB_PASSWORD=%s`, shellEscape(s.dbPassword)),
		fmt.Sprintf(`STEPANEL_ACCOUNT_KEY=%s`, shellEscape(s.accountKey)),
		fmt.Sprintf(`STEPANEL_BACKUP_SIGNING_KEY=%s`, shellEscape(s.backupSigningKey)),
		fmt.Sprintf(`STEPANEL_ENVIRONMENT_KEY=%s`, shellEscape(s.environmentKey)),
		fmt.Sprintf(`STEPANEL_GIT_WEBHOOK_SECRET=%s`, shellEscape(s.gitWebhookSecret)),
	}

	if s.tlsCertFile != "" {
		lines = append(lines, fmt.Sprintf(`STEPANEL_TLS_CERT_FILE=%s`, shellEscape(s.tlsCertFile)))
		lines = append(lines, fmt.Sprintf(`STEPANEL_TLS_KEY_FILE=%s`, shellEscape(s.tlsKeyFile)))
	}

	if s.requireOffsiteBackup {
		lines = append(lines, "STEPANEL_REQUIRE_OFFSITE_BACKUP=1")
	}

	if s.production {
		lines = append(lines, "STEPANEL_ENV=production")
	}

	content := strings.Join(lines, "\n") + "\n"

	if err := writeAtomic(outputPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("write configuration file: %w", err)
	}

	fmt.Printf("✓ Configuration written to %s\n", outputPath)

	savedEnv := make(map[string]string)
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := parts[0]
			val := strings.Trim(parts[1], `"`)
			savedEnv[key] = os.Getenv(key)
			os.Setenv(key, val)
		}
	}
	defer func() {
		for key, val := range savedEnv {
			if val == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, val)
			}
		}
	}()

	cfg := LoadConfig()
	if err := ValidateConfig(cfg); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}

	fmt.Println("✓ Configuration validated successfully")
	fmt.Printf("\nTo activate this configuration:\n")
	fmt.Printf("  source %s\n", outputPath)
	fmt.Printf("  stepanel\n")
	return nil
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
