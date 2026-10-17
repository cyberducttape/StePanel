package rootbroker

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzValidateRequest drives arbitrary JSON through the broker's request type
// and validator, as the socket server does for every peer request. Validation
// must never panic, including on missing or partial operation payloads, and an
// accepted site request must name a valid site.
func FuzzValidateRequest(f *testing.F) {
	for _, seed := range []string{
		`{"type":"health"}`,
		`{"type":"site","site":{"action":"create","site":"demo"}}`,
		`{"type":"site","site":{"action":"delete","site":"../etc"}}`,
		`{"type":"site"}`,
		`{"type":"db","db":{"action":"restore-dump","database":"demo_wp","site":"demo","dump_path":"../../etc/shadow"}}`,
		`{"type":"vhost","vhost":{"action":"apply","site":"demo","domain":"demo.example.test","webserver":"caddy"}}`,
		`{"type":"vhost","vhost":{"action":"delete","site":"demo","name":"demo.caddy","webserver":"caddy"}}`,
		`{"type":"git","git":{}}`,
		`{"type":"task","task":{"site":"demo"}}`,
		`{"type":"wordpress"}`,
		`{"type":"unknown"}`,
	} {
		f.Add([]byte(seed))
	}
	v := NewValidator(f.TempDir())
	f.Fuzz(func(t *testing.T, data []byte) {
		var req Request
		if json.Unmarshal(data, &req) != nil {
			return
		}
		if err := v.ValidateRequest(&req); err != nil {
			return
		}
		if req.RequestType == "site" {
			if req.Site == nil || v.ValidateSiteName(req.Site.Site) != nil {
				t.Fatalf("accepted site request without a valid site: %s", data)
			}
		}
	})
}

// FuzzValidateFilePath checks that every accepted relative path stays inside
// the root it was validated against.
func FuzzValidateFilePath(f *testing.F) {
	for _, seed := range []string{"public/index.php", "a/../../etc", "..", "/etc/passwd", "a//b", "./x", "sites/demo", "a\x00b", "...."} {
		f.Add(seed)
	}
	root := f.TempDir()
	v := NewValidator(root)
	f.Fuzz(func(t *testing.T, path string) {
		if v.ValidateFilePath(root, path) != nil {
			return
		}
		if filepath.IsAbs(path) || strings.Contains(path, "..") {
			t.Fatalf("accepted absolute or traversing path %q", path)
		}
		joined := filepath.Join(root, path)
		if joined != root && !strings.HasPrefix(joined, root+string(filepath.Separator)) {
			t.Fatalf("accepted path %q resolves outside root: %s", path, joined)
		}
	})
}
