package main

import (
	"fmt"
	"os"

	setupwiz "github.com/cyberducttape/StePanel/internal/setup"
)

func runSetupWizard() {
	fmt.Println()
	fmt.Println("╔════════════════════════════════════════╗")
	fmt.Println("║   StePanel First-Run Setup Wizard      ║")
	fmt.Println("╚════════════════════════════════════════╝")
	fmt.Println()

	wizard := setupwiz.NewWizard(os.Stdin, os.Stdout)
	_, err := wizard.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Setup failed: %v\n", err)
		os.Exit(1)
	}
}
