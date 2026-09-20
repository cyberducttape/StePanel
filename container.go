package main

import (
	"errors"
	"regexp"
	"strings"
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

// imageRefPattern matches OCI image references with optional hash
// Format: [registry/]namespace/repository:tag[@sha256:hash]
var imageRefPattern = regexp.MustCompile(
	`^(?:([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)*[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(?::[0-9]{1,5})?/)?` +
		`([a-z0-9]+(?:[._-][a-z0-9]+)*/)?` +
		`([a-z0-9]+(?:[._-][a-z0-9]+)*)` +
		`(?::([a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}))?` +
		`(?:@sha256:([a-f0-9]{64}))?$`,
)

// ParseContainerImage parses and validates a container image reference
func ParseContainerImage(image string, allowedRegistries map[string]bool) (*ContainerImageRef, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, errors.New("image reference cannot be empty")
	}

	// Split into registry and remainder
	var registry, remainder string

	// Check if there's an explicit registry (has . or : in first component)
	parts := strings.Split(image, "/")
	if len(parts) > 0 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		registry = parts[0]
		remainder = strings.Join(parts[1:], "/")
	} else {
		registry = "docker.io"
		remainder = image
	}

	// Validate registry is allowed
	if !allowedRegistries[registry] {
		return nil, errors.New("container registry not allowed: " + registry)
	}

	// Parse namespace/repository:tag[@hash]
	var namespace, repository, tag, hash string

	// Extract hash if present
	if strings.Contains(remainder, "@") {
		hashParts := strings.Split(remainder, "@")
		remainder = hashParts[0]
		hashRef := hashParts[1]
		if strings.HasPrefix(hashRef, "sha256:") && len(hashRef) == 71 { // sha256: + 64 hex
			hash = hashRef
		} else {
			return nil, errors.New("invalid image hash format")
		}
	}

	// Extract tag if present
	if strings.Contains(remainder, ":") {
		tagParts := strings.Split(remainder, ":")
		remainder = tagParts[0]
		tag = tagParts[1]
		if tag == "" {
			tag = "latest"
		}
	} else {
		tag = "latest"
	}

	// Parse namespace/repository
	parts = strings.Split(remainder, "/")
	if len(parts) == 1 {
		// library/image format for docker.io
		if registry == "docker.io" {
			namespace = "library"
			repository = parts[0]
		} else {
			return nil, errors.New("image reference requires namespace")
		}
	} else if len(parts) == 2 {
		namespace = parts[0]
		repository = parts[1]
	} else {
		return nil, errors.New("invalid image reference format")
	}

	// Validate components
	if repository == "" {
		return nil, errors.New("image reference requires repository")
	}

	// Basic validation of component names
	if !isValidImageComponent(namespace) {
		return nil, errors.New("invalid namespace")
	}
	if !isValidImageComponent(repository) {
		return nil, errors.New("invalid repository")
	}
	if !isValidImageComponent(tag) {
		return nil, errors.New("invalid tag")
	}

	return &ContainerImageRef{
		Registry:   registry,
		Namespace:  namespace,
		Repository: repository,
		Tag:        tag,
		Hash:       hash,
	}, nil
}

// isValidImageComponent validates a component name (namespace, repository, tag)
func isValidImageComponent(component string) bool {
	if component == "" {
		return false
	}
	// Allow lowercase letters, digits, and separators
	for _, ch := range component {
		if !((ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '_' || ch == '.') {
			return false
		}
	}
	return true
}

// ValidateContainerImageForSite validates a container image for deployment in a site
func ValidateContainerImageForSite(site, image string) (*ContainerImageRef, error) {
	// TODO: Load site-specific allowlist from database if available
	// For now, use default allowlist
	return ParseContainerImage(image, AllowedContainerRegistries)
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
