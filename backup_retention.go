package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

func pruneSiteBackups(root string, site SiteCapability, keep int) error {
	siteName := site.Site()
	if safeUser(siteName) == "" || keep < 1 {
		return errors.New("invalid backup retention policy")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	type candidate struct{ name, path string }
	items := []candidate{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name()[0] == '.' {
			continue
		}
		path := filepath.Join(root, entry.Name())
		// Retention must still be able to remove stale artifacts when their
		// archive is damaged; full manifest/archive validation belongs to the
		// inventory and restore paths.
		data, readErr := os.ReadFile(filepath.Join(path, "manifest.json"))
		var manifest struct {
			Site string `json:"site"`
		}
		if readErr == nil {
			readErr = json.Unmarshal(data, &manifest)
		}
		if readErr == nil && manifest.Site == siteName {
			items = append(items, candidate{entry.Name(), path})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name > items[j].name })
	if len(items) <= keep {
		return syncDirectory(root)
	}
	for _, item := range items[keep:] {
		if err := os.RemoveAll(item.path); err != nil {
			return err
		}
	}
	return syncDirectory(root)
}
