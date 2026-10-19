package stepanel

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOptionalNodeFileRejectsOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".nvmrc")
	if err := os.WriteFile(path, make([]byte, maxNodeVersionFileBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := readOptionalFile(path, maxNodeVersionFileBytes); !errors.Is(err, errStateFileTooLarge) || exists {
		t.Fatalf("oversized Node version file = exists %t, err %v; want a bounded-read error", exists, err)
	}
}
