package stepanel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

// Without the broker, the development account helper is the only check
// between request names and filesystem paths: it must apply the broker's
// name rules and never create anything outside the home root.
func TestDevelopmentAccountHelperRefusesNamesThatEscapeTheHomeRoot(t *testing.T) {
	homeRoot := filepath.Join(t.TempDir(), "home")
	cfg := Config{HomeRoot: homeRoot}
	for _, req := range []rootbroker.AccountRequest{
		{Action: "ensure", Account: "../escape"},
		{Action: "ensure", Account: "/abs"},
		{Action: "place", Account: "alice", Site: "../../escape", Kind: "main", Directory: "public_html"},
		{Action: "place", Account: "alice", Site: "a/b", Kind: "main", Directory: "public_html"},
		{Action: "place", Account: "..", Site: "alice", Kind: "main", Directory: "public_html"},
	} {
		if _, err := accountHelperContext(context.Background(), cfg, req); err == nil {
			t.Fatalf("%+v was accepted", req)
		}
	}
	if _, err := os.Stat(filepath.Dir(homeRoot)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(homeRoot))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused requests created %v", entries)
	}

	if _, err := accountHelperContext(context.Background(), cfg, rootbroker.AccountRequest{Action: "place", Account: "alice", Site: "alice", Kind: "main", Directory: "public_html"}); err != nil {
		t.Fatalf("valid placement refused: %v", err)
	}
	if info, err := os.Stat(filepath.Join(homeRoot, "alice", ".stepanel", "sites", "alice", "php")); err != nil || !info.IsDir() {
		t.Fatalf("valid placement state = %v, %v", info, err)
	}
}
