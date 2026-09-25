package main

import (
	"testing"
)

func TestParseContainerImage(t *testing.T) {
	tests := []struct {
		image      string
		shouldFail bool
		expected   *ContainerImageRef
		desc       string
	}{
		// Valid: Public registries
		{
			"alpine",
			false,
			&ContainerImageRef{"docker.io", "library", "alpine", "latest", ""},
			"docker.io implicit, default tag",
		},
		{
			"alpine:3.17",
			false,
			&ContainerImageRef{"docker.io", "library", "alpine", "3.17", ""},
			"docker.io implicit, explicit tag",
		},
		{
			"docker.io/library/alpine:3.17",
			false,
			&ContainerImageRef{"docker.io", "library", "alpine", "3.17", ""},
			"docker.io explicit",
		},
		{
			"ghcr.io/myorg/myapp:v1.0.0",
			false,
			&ContainerImageRef{"ghcr.io", "myorg", "myapp", "v1.0.0", ""},
			"ghcr.io with version",
		},
		{
			"quay.io/coreos/etcd:v3.5.0",
			false,
			&ContainerImageRef{"quay.io", "coreos", "etcd", "v3.5.0", ""},
			"quay.io etcd",
		},
		{
			"docker.io/library/ubuntu:20.04@sha256:" +
				"a5f43f9da1b8d2a9e77d4a8f4e8f8f8e8f8f8f8e8f8f8e8f8f8e8f8f8f8f1234",
			false,
			&ContainerImageRef{
				"docker.io",
				"library",
				"ubuntu",
				"20.04",
				"sha256:a5f43f9da1b8d2a9e77d4a8f4e8f8f8e8f8f8f8e8f8f8e8f8f8e8f8f8f8f1234",
			},
			"with sha256 hash",
		},

		// Invalid: Blocked registries
		{
			"attacker.com/malware:latest",
			true,
			nil,
			"attacker registry blocked",
		},
		{
			"registry.internal.example.com/image:tag",
			true,
			nil,
			"private registry blocked",
		},
		{
			"localhost:5000/local/image",
			true,
			nil,
			"localhost registry blocked",
		},
		{
			"127.0.0.1:5000/image",
			true,
			nil,
			"loopback IP blocked",
		},
		{
			"evil-registry.com/app:latest",
			true,
			nil,
			"unallowed registry blocked",
		},

		// Invalid: Malformed
		{
			"",
			true,
			nil,
			"empty image",
		},
		{
			"just-a-tag",
			false,
			&ContainerImageRef{"docker.io", "library", "just-a-tag", "latest", ""},
			"single component treated as image name",
		},
		{
			"alpine:@sha256:invalid",
			true,
			nil,
			"invalid hash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			ref, err := ParseContainerImage(tt.image, AllowedContainerRegistries)

			if (err != nil) != tt.shouldFail {
				t.Fatalf("ParseContainerImage(%q): shouldFail=%v, got err=%v",
					tt.image, tt.shouldFail, err)
			}

			if !tt.shouldFail && ref == nil {
				t.Fatalf("ParseContainerImage(%q): expected result, got nil", tt.image)
			}

			if !tt.shouldFail && ref != nil {
				if ref.Registry != tt.expected.Registry {
					t.Errorf("Registry: got %s, expected %s", ref.Registry, tt.expected.Registry)
				}
				if ref.Namespace != tt.expected.Namespace {
					t.Errorf("Namespace: got %s, expected %s", ref.Namespace, tt.expected.Namespace)
				}
				if ref.Repository != tt.expected.Repository {
					t.Errorf("Repository: got %s, expected %s", ref.Repository, tt.expected.Repository)
				}
				if ref.Tag != tt.expected.Tag {
					t.Errorf("Tag: got %s, expected %s", ref.Tag, tt.expected.Tag)
				}
				if ref.Hash != tt.expected.Hash {
					t.Errorf("Hash: got %s, expected %s", ref.Hash, tt.expected.Hash)
				}
			}
		})
	}
}

// TestImageAllowedByPatterns covers the STEPANEL_RUNNER_ALLOWED_IMAGES
// narrowing check added to close the "allow ghcr.io → any image on ghcr.io"
// gap in the registry-wide allowlist. The check is strict-segment, not
// substring, so near-match namespaces cannot slip through.
func TestImageAllowedByPatterns(t *testing.T) {
	ref := &ContainerImageRef{Registry: "ghcr.io", Namespace: "anthropic", Repository: "builder", Tag: "v1"}
	// Empty patterns → the check is opt-in and returns true (unconstrained).
	if !ImageAllowedByPatterns(ref, nil) {
		t.Error("empty patterns should return true")
	}
	// Exact match.
	if !ImageAllowedByPatterns(ref, []string{"ghcr.io/anthropic/builder"}) {
		t.Error("exact match failed")
	}
	// Namespace wildcard.
	if !ImageAllowedByPatterns(ref, []string{"ghcr.io/anthropic/*"}) {
		t.Error("namespace wildcard failed")
	}
	// Wildcard is case-insensitive (config is lowercased on load).
	if !ImageAllowedByPatterns(ref, []string{"GHCR.IO/ANTHROPIC/*"}) {
		t.Error("wildcard case-insensitivity failed")
	}
	// Different namespace on the same registry is rejected — this is the
	// exact gap the check closes: allowing ghcr.io alone would authorize
	// the attacker's image below; the narrow allowlist refuses it.
	attacker := &ContainerImageRef{Registry: "ghcr.io", Namespace: "attacker", Repository: "builder", Tag: "v1"}
	if ImageAllowedByPatterns(attacker, []string{"ghcr.io/anthropic/*"}) {
		t.Error("different namespace should be refused")
	}
	// Prefix-collision namespace ("anthropic-evil" starts with "anthropic")
	// must not match "ghcr.io/anthropic/*" — the strip is "/*" (trailing
	// two chars), so the compared prefix is "ghcr.io/anthropic/" with the
	// trailing slash. "anthropic-evil" doesn't have that slash after
	// "anthropic" so it fails, as intended.
	sneaky := &ContainerImageRef{Registry: "ghcr.io", Namespace: "anthropic-evil", Repository: "builder", Tag: "v1"}
	if ImageAllowedByPatterns(sneaky, []string{"ghcr.io/anthropic/*"}) {
		t.Error("prefix-collision namespace must not match")
	}
	// Wrong registry with a valid namespace is refused.
	wrongRegistry := &ContainerImageRef{Registry: "docker.io", Namespace: "anthropic", Repository: "builder", Tag: "v1"}
	if ImageAllowedByPatterns(wrongRegistry, []string{"ghcr.io/anthropic/*"}) {
		t.Error("wrong registry must not match")
	}
	// Multiple patterns, one matches.
	if !ImageAllowedByPatterns(ref, []string{"docker.io/library/*", "ghcr.io/anthropic/*"}) {
		t.Error("multi-pattern OR match failed")
	}
	// Nil ref rejected.
	if ImageAllowedByPatterns(nil, []string{"ghcr.io/anthropic/*"}) {
		t.Error("nil ref must not match anything")
	}
}

func TestParseImagePatternList(t *testing.T) {
	if got := ParseImagePatternList(""); got != nil {
		t.Errorf("empty input got %v, want nil", got)
	}
	if got := ParseImagePatternList("  ,  , "); got != nil {
		t.Errorf("whitespace-only input got %v, want nil", got)
	}
	got := ParseImagePatternList("ghcr.io/a/*, docker.io/library/alpine ")
	want := []string{"ghcr.io/a/*", "docker.io/library/alpine"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestValidateContainerImageForSite(t *testing.T) {
	tests := []struct {
		image      string
		shouldFail bool
		desc       string
	}{
		{"alpine", false, "public image allowed"},
		{"ghcr.io/kubernetes/kube-apiserver:v1.27.0", false, "kubernetes image allowed"},
		{"quay.io/prometheus/prometheus:latest", false, "prometheus allowed"},
		{"evil.com/malware:latest", true, "malicious registry blocked"},
		{"registry.internal/app:v1.0", true, "private registry blocked"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			_, err := ValidateContainerImageForSite(tt.image, AllowedContainerRegistries)
			if (err != nil) != tt.shouldFail {
				t.Errorf("shouldFail=%v, got err=%v", tt.shouldFail, err)
			}
		})
	}
}

func TestImageString(t *testing.T) {
	ref := &ContainerImageRef{
		Registry:   "ghcr.io",
		Namespace:  "myorg",
		Repository: "myapp",
		Tag:        "v1.0.0",
		Hash:       "",
	}

	expected := "ghcr.io/myorg/myapp:v1.0.0"
	if result := ref.ImageString(); result != expected {
		t.Errorf("ImageString(): got %s, expected %s", result, expected)
	}

	ref.Hash = "sha256:abcd1234567890abcd1234567890abcd1234567890abcd1234567890abcd1234"
	expected = "ghcr.io/myorg/myapp:v1.0.0@sha256:abcd1234567890abcd1234567890abcd1234567890abcd1234567890abcd1234"
	if result := ref.ImageString(); result != expected {
		t.Errorf("ImageString() with hash: got %s, expected %s", result, expected)
	}
}

func TestIsPublicRegistry(t *testing.T) {
	tests := []struct {
		registry string
		isPublic bool
		desc     string
	}{
		{"docker.io", true, "docker.io is public"},
		{"ghcr.io", true, "ghcr.io is public"},
		{"quay.io", true, "quay.io is public"},
		{"registry.example.com", false, "private registry"},
		{"localhost:5000", false, "localhost not public"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			ref := &ContainerImageRef{Registry: tt.registry}
			if result := ref.IsPublicRegistry(); result != tt.isPublic {
				t.Errorf("IsPublicRegistry(): got %v, expected %v", result, tt.isPublic)
			}
		})
	}
}
