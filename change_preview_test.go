package stepanel

import (
	"strings"
	"testing"
)

func TestChangePreviewIncludesBeforeAfterValidationInterruptionAndProof(t *testing.T) {
	app := &App{Auth: Auth{Secret: "test-change-preview-signing-secret"}}
	before := map[string]any{"version": "8.2", "memory_limit": "256M", "state": "applied"}
	proposed := map[string]any{"site": "example", "version": "8.3", "memory_limit": "512M"}
	preview, err := app.makeChangePreview("php.runtime:example", before, proposed, []string{"reload PHP-FPM"}, []string{"restore the previous profile"})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Before.(map[string]any)["version"] != "8.2" || preview.Proposed.(map[string]any)["version"] != "8.3" {
		t.Fatalf("preview lost the configuration diff: before=%#v after=%#v", preview.Before, preview.Proposed)
	}
	if len(preview.ValidationCommands) < 3 || len(preview.InterruptionPoints) < 2 || len(preview.DesiredStateProof) == 0 {
		t.Fatalf("preview missing execution evidence: %+v", preview)
	}
	joined := strings.Join(func() []string {
		items := make([]string, 0, len(preview.ValidationCommands))
		for _, item := range preview.ValidationCommands {
			items = append(items, item.Command)
		}
		return items
	}(), "\n")
	if !strings.Contains(joined, "stepanelctl site inspect example") {
		t.Fatalf("preview has no exact post-change inspection command: %s", joined)
	}
	if !app.verifyChangePreview(preview.PreviewToken, "php.runtime:example", before, proposed) {
		t.Fatal("adding review evidence invalidated the signed before/after preview")
	}
}

func TestChangePreviewExplainsDomainConfigValidationAndInterruption(t *testing.T) {
	app := &App{Auth: Auth{Secret: "test-change-preview-signing-secret"}, Config: Config{WebServer: "caddy"}}
	preview, err := app.makeChangePreview("domain.route.apply:site-example_test.caddy", map[string]any{"sha256": "old"}, map[string]any{"site": "example", "domain": "example.test", "web_server": "caddy", "vhost_path": "/etc/caddy/sites/example.caddy"}, nil, []string{"restore prior snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	commands := []string{}
	for _, item := range preview.ValidationCommands {
		commands = append(commands, item.Command)
	}
	if !strings.Contains(strings.Join(commands, "\n"), "caddy validate --config /etc/caddy/Caddyfile") {
		t.Fatalf("preview missing Caddy validation command: %v", commands)
	}
	if !strings.Contains(preview.InterruptionPoints[1].Expected, "snapshot") || !strings.Contains(preview.InterruptionPoints[1].Recovery, "web-server logs") {
		t.Fatalf("preview missing interruption recovery contract: %+v", preview.InterruptionPoints)
	}
}
