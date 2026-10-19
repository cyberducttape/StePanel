package stepanel

import (
	statefile "github.com/cyberducttape/StePanel/internal/state"
)

var errStateFileTooLarge = statefile.ErrFileTooLarge

// readStateFile caps memory use for JSON-backed local state. LimitReader keeps
// the bound effective even if the file grows after it is opened.
func readStateFile(path string, maxBytes int64) ([]byte, error) {
	return statefile.ReadFileBounded(path, maxBytes)
}
