package main

import (
	"fmt"
	"os"

	setupwiz "github.com/itchyitchy123/StePanel/internal/setup"
)

func runSetupWizard() {
	fmt.Println()
	fmt.Println("╔════════════════════════════════════════╗")
	fmt.Println("║   StePanel First-Run Setup Wizard      ║")
	fmt.Println("╚════════════════════════════════════════╝")
	fmt.Println()

	wizard := setupwiz.NewWizard(os.Stdin, os.Stdout)
	cfg, err := wizard.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Setup failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("📋 Configuration Summary:")
	fmt.Println()
	fmt.Println("Add these to your environment or .env file:")
	fmt.Println()
	fmt.Printf("STEPANEL_ADMIN_USERNAME=%s\n", cfg.AdminUsername)
	fmt.Println("STEPANEL_ADMIN_PASSWORD_HASH=<run-setup-wizard-to-generate>")
	fmt.Printf("STEPANEL_SESSION_SECRET=%s\n", cfg.SessionSecret)
	if cfg.Production {
		fmt.Printf("STEPANEL_TLS_CERT_FILE=%s\n", cfg.TLSCert)
		fmt.Printf("STEPANEL_TLS_KEY_FILE=%s\n", cfg.TLSKey)
		fmt.Printf("STEPANEL_LISTEN=%s\n", cfg.Listen)
		fmt.Println("STEPANEL_PRODUCTION=true")
	}
	fmt.Printf("STEPANEL_WEB_ROOT=%s\n", cfg.WebRoot)
	fmt.Printf("STEPANEL_BACKUP_ROOT=%s\n", cfg.BackupRoot)
	fmt.Println()
	fmt.Println("✅ Setup wizard completed successfully!")
	fmt.Println()
}
