package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/distribution/reference"
)

// AllowedContainerRegistries is the default set of allowed registries
var AllowedContainerRegistries = map[string]bool{
	"docker.io": true,
	"ghcr.io":   true,
	"quay.io":   true,
}

// ContainerImageRef represents a parsed container image reference
type ContainerImageRef struct {
	Registry   string // docker.io, ghcr.io, quay.io
	Namespace  string // library, myorg, coreos
	Repository string // alpine, postgres, etcd
	Tag        string // latest, v1.2.3
	Hash       string // Optional: sha256:deadbeef...
}

// ParseContainerImage parses and validates a container image reference
func ParseContainerImage(image string, allowedRegistries map[string]bool) (*ContainerImageRef, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, errors.New("image reference cannot be empty")
	}
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return nil, fmt.Errorf("invalid image reference: %w", err)
	}
	registry := strings.ToLower(reference.Domain(named))
	if !containerRegistryAllowed(registry, allowedRegistries) {
		return nil, errors.New("container registry not allowed: " + registry)
	}
	pathParts := strings.Split(reference.Path(named), "/")
	if len(pathParts) == 0 || pathParts[len(pathParts)-1] == "" {
		return nil, errors.New("image reference requires repository")
	}
	repository := pathParts[len(pathParts)-1]
	namespace := strings.Join(pathParts[:len(pathParts)-1], "/")
	if namespace == "" {
		namespace = "library"
	}
	tag := "latest"
	if tagged, ok := named.(reference.Tagged); ok {
		tag = tagged.Tag()
	}
	hash := ""
	if digested, ok := named.(reference.Digested); ok {
		hash = digested.Digest().String()
		if !strings.HasPrefix(hash, "sha256:") {
			return nil, errors.New("image digest must use sha256")
		}
	}

	return &ContainerImageRef{
		Registry:   registry,
		Namespace:  namespace,
		Repository: repository,
		Tag:        tag,
		Hash:       hash,
	}, nil
}

func containerRegistryAllowed(registry string, allowed map[string]bool) bool {
	for candidate, ok := range allowed {
		if ok && strings.EqualFold(strings.TrimSpace(candidate), registry) {
			return true
		}
	}
	return false
}

// ValidateContainerImageForSite validates a container image for deployment in a site
// Uses the provided allowed registries list (typically from config)
func ValidateContainerImageForSite(image string, allowedRegistries map[string]bool) (*ContainerImageRef, error) {
	ref, err := ParseContainerImage(image, allowedRegistries)
	if err != nil {
		return nil, err
	}
	if ref.Hash == "" {
		return nil, errors.New("container image digest is required")
	}
	return ref, nil
}

// ImageAllowedByPatterns reports whether ref satisfies at least one entry
// in patterns. Each pattern is either an exact "registry/namespace/repo"
// or a trailing-wildcard "registry/namespace/*" that authorizes any
// repository under a namespace.
//
// An empty patterns slice returns true — the check is opt-in and callers
// should skip it when RunnerAllowedImages is not configured. All matching
// is lowercase and exact-segment (no substring, no other glob
// metacharacters), mirroring the tight constraint the operator opts into
// when they choose to configure it.
func ImageAllowedByPatterns(ref *ContainerImageRef, patterns []string) bool {
	if ref == nil {
		return false
	}
	if len(patterns) == 0 {
		return true
	}
	actual := strings.ToLower(ref.Registry + "/" + ref.Namespace + "/" + ref.Repository)
	namespacePrefix := strings.ToLower(ref.Registry + "/" + ref.Namespace + "/")
	for _, raw := range patterns {
		pattern := strings.TrimSpace(strings.ToLower(raw))
		if pattern == "" {
			continue
		}
		if strings.HasSuffix(pattern, "/*") {
			if strings.TrimSuffix(pattern, "*") == namespacePrefix {
				return true
			}
			continue
		}
		if pattern == actual {
			return true
		}
	}
	return false
}

// ParseImagePatternList splits a comma-separated STEPANEL_RUNNER_ALLOWED_IMAGES
// value into a slice, dropping empty and whitespace-only entries. Returns
// nil (not an empty slice) when no non-empty entries remain, so a
// `len == 0` check by callers is equivalent to `pattern list is unset`.
func ParseImagePatternList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ImageString returns the full image reference as a string
func (cir *ContainerImageRef) ImageString() string {
	result := cir.Registry + "/" + cir.Namespace + "/" + cir.Repository + ":" + cir.Tag
	if cir.Hash != "" {
		result += "@" + cir.Hash
	}
	return result
}

// IsPublicRegistry returns true if the image uses a public registry
func (cir *ContainerImageRef) IsPublicRegistry() bool {
	publicRegistries := map[string]bool{
		"docker.io": true,
		"ghcr.io":   true,
		"quay.io":   true,
	}
	return publicRegistries[cir.Registry]
}
