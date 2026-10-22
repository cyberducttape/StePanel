package stepanel

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
)

// webAssets makes the production binary self-contained. Deployments no longer
// depend on a particular working directory or a separately synchronized copy
// of the dashboard assets.
//
//go:embed web/index.html web/panel.html web/partials.html web/static/*
var webAssets embed.FS

// loadViews parses the operator dashboard and the customer panel, each with
// the shared partials.
func loadViews() (dashboard, panel *template.Template, err error) {
	parse := func(page string) (*template.Template, error) {
		return template.New(page).Funcs(template.FuncMap{"add": func(a, b int) int { return a + b }}).ParseFS(webAssets, "web/"+page, "web/partials.html")
	}
	if dashboard, err = parse("index.html"); err != nil {
		return nil, nil, fmt.Errorf("parse dashboard: %w", err)
	}
	if panel, err = parse("panel.html"); err != nil {
		return nil, nil, fmt.Errorf("parse customer panel: %w", err)
	}
	return dashboard, panel, nil
}

// embeddedAssetVersion fingerprints all dashboard assets compiled into the
// binary. It is added to static URLs so a newly deployed binary never reuses
// an old browser-cached stylesheet or JavaScript bundle.
func embeddedAssetVersion() (string, error) {
	hash := sha256.New()
	err := fs.WalkDir(webAssets, "web/static", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := webAssets.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}
