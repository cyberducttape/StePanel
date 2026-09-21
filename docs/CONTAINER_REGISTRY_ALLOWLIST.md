# Container Registry Allowlist: Supply Chain Security

## Problem

Current implementation (v0.7.0):
```dockerfile
# UNSAFE: Pull from any registry
FROM some-registry.example.com/malicious/image:latest
RUN apt-get install malware
```

**Risk**: Malicious container image = arbitrary code execution as root

## Solution: Registry Allowlist + Image Validation

### 1. Allowed Registries (Default)

Only three public registries allowed by default:

```
docker.io              # Docker Hub (official images)
ghcr.io                # GitHub Container Registry
quay.io                # Quay.io (Kubernetes/CoreOS ecosystem)
```

**NOT allowed**:
- Private registries (registry.example.com)
- Localhost registries (localhost:5000)
- Self-signed registries (assumed malicious)
- Artifact repositories (artifactory.example.com)

### 2. Image Validation

Check before pulling:

```go
type ContainerImage struct {
    Registry   string    // docker.io, ghcr.io, quay.io
    Namespace  string    // library, myorg, coreos
    Repository string    // alpine, postgres, etcd
    Tag        string    // latest, v1.2.3, main-sha123
    Hash       string    // Optional: sha256:deadbeef...
}

func ValidateContainerImage(image string) (*ContainerImage, error) {
    // 1. Parse image name
    // 2. Verify registry is in allowlist
    // 3. Validate image size limit (optional)
    // 4. Verify hash if provided (optional)
    // 5. Return validated image or error
}
```

### 3. Per-Site Overrides (Optional)

Allow administrator to expand allowlist per-site:

```yaml
sites:
  mysite:
    container:
      allowed_registries:
        - docker.io
        - ghcr.io
        - quay.io
        # Site-specific addition:
        - registry.mycompany.com   # Requires admin approval
      image_size_limit: 5GB
      require_image_hash: false
```

### 4. Image Size Limits

Prevent disk exhaustion from large images:

```
Default: 5 GB per image
Configurable per site
Checked before pull
```

## Implementation

### Step 1: Image Registry Validation

Add to platform/resources.go or new file platform/container.go:

```go
package main

import (
	"errors"
	"regexp"
	"strings"
)

var (
	// Official allowed registries
	allowedRegistries = map[string]bool{
		"docker.io":  true,
		"ghcr.io":    true,
		"quay.io":    true,
	}

	// Image reference format: [registry/]namespace/repository:tag[@hash]
	imageRefPattern = regexp.MustCompile(
		`^(?:([a-z0-9.-]+(?:\.[a-z0-9]+)*|[a-z0-9]+(?:[._-][a-z0-9]+)*)/)` +
		`([a-z0-9]+(?:[._-][a-z0-9]+)*)` +
		`(?:/([a-z0-9]+(?:[._-][a-z0-9]+)*))` +
		`(?::([a-z0-9]+(?:[._-][a-z0-9]+)*|[a-zA-Z0-9]{7,40}))?` +
		`(?:@sha256:([a-f0-9]{64}))?$`,
	)
)

type ContainerImageRef struct {
	Registry   string // docker.io, ghcr.io, quay.io, or "" (docker.io implicit)
	Namespace  string // library, myorg
	Repository string // alpine, postgres
	Tag        string // latest, v1.2.3
	Hash       string // Optional SHA256 hash
}

// ParseContainerImage parses and validates a container image reference
func ParseContainerImage(image string, allowedRegistries []string) (*ContainerImageRef, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, errors.New("image reference cannot be empty")
	}

	// Default to docker.io if no registry specified
	if !strings.Contains(image[:strings.LastIndex(image, "/")], ".") &&
		!strings.Contains(image[:strings.LastIndex(image, "/")], ":") {
		image = "docker.io/" + image
	}

	parts := strings.Split(image, "/")
	if len(parts) < 2 || len(parts) > 4 {
		return nil, errors.New("invalid image reference format")
	}

	var registry, namespace, repository, tagAndHash string

	if len(parts) == 2 {
		registry = "docker.io"
		namespace = "library"
		repository = parts[0]
		tagAndHash = parts[1]
	} else if len(parts) == 3 {
		// Determine if first part is registry or namespace
		if strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") {
			registry = parts[0]
			namespace = parts[1]
			repository = parts[2]
		} else {
			registry = "docker.io"
			namespace = parts[0]
			repository = parts[1]
			tagAndHash = parts[2]
		}
	} else { // len(parts) == 4
		registry = parts[0]
		namespace = parts[1]
		repository = parts[2]
		tagAndHash = parts[3]
	}

	// Extract tag and hash
	var tag, hash string
	if strings.Contains(tagAndHash, "@") {
		parts := strings.Split(tagAndHash, "@")
		tag = parts[0]
		hash = parts[1]
		if !strings.HasPrefix(hash, "sha256:") || len(hash) != 71 {
			return nil, errors.New("invalid image hash format")
		}
	} else {
		tag = tagAndHash
		if tag == "" {
			tag = "latest"
		}
	}

	if tag == "" {
		tag = "latest"
	}

	// Validate registry against allowlist
	if _, allowed := allowedRegistries[registry]; !allowed {
		return nil, errors.New("registry " + registry + " is not in allowlist")
	}

	return &ContainerImageRef{
		Registry:   registry,
		Namespace:  namespace,
		Repository: repository,
		Tag:        tag,
		Hash:       hash,
	}, nil
}

// ValidateContainerImageForSite validates an image for a specific site
func ValidateContainerImageForSite(cfg Config, site, image string) (*ContainerImageRef, error) {
	allowed := []string{"docker.io", "ghcr.io", "quay.io"}

	// TODO: Load site-specific allowlist from database if available
	// if siteConfig := cfg.GetSiteContainerConfig(site); siteConfig != nil {
	//     allowed = siteConfig.AllowedRegistries
	// }

	return ParseContainerImage(image, allowed)
}

// ImageSizeValid checks if an image would exceed the size limit
func ImageSizeValid(imageSizeBytes int64, cfg Config) bool {
	const defaultLimitGB = 5
	limitBytes := int64(defaultLimitGB * 1024 * 1024 * 1024)

	// TODO: Load site-specific limit if available
	// if siteConfig := cfg.GetSiteContainerConfig(site); siteConfig != nil {
	//     limitBytes = siteConfig.ImageSizeLimit
	// }

	return imageSizeBytes <= limitBytes
}
```

### Step 2: Validation Points

Add image validation at container operations:

**In Dockerfile processing** (if building):
```go
func (a *App) validateDockerfileImages(site, dockerfile string) error {
	// Parse Dockerfile for FROM statements
	// Validate each image reference
	// Return error if any image not allowed
}
```

**In docker run/pull** (if pulling):
```go
func (a *App) validateContainerPull(site, image string) error {
	ref, err := ValidateContainerImageForSite(a.Config, site, image)
	if err != nil {
		return fmt.Errorf("container image not allowed: %w", err)
	}
	if !ImageSizeValid(ref, a.Config) {
		return errors.New("container image exceeds size limit")
	}
	return nil
}
```

### Step 3: Configuration

```yaml
# /etc/stepanel/config.yaml
container:
  # Allowlisted registries (default below)
  allowed_registries:
    - docker.io
    - ghcr.io
    - quay.io
  
  # Size limits
  image_size_limit_gb: 5
  
  # Enforcement
  require_image_hash: false      # Future: mandate SHA256 hashes
  network_access: false          # Block registry access from helper
  
  # Overrides per site
  site_overrides:
    premium-site:
      allowed_registries:
        - docker.io
        - ghcr.io
        - quay.io
        - registry.mycompany.com
      image_size_limit_gb: 10
```

## Security Boundaries

### What's Protected
✅ Blocks malicious public images (attacker.com/malware)  
✅ Blocks private registries (assumed untrusted)  
✅ Blocks localhost registries (local attack vector)  
✅ Enforces size limits (prevents disk exhaustion)  

### What's NOT Protected (Separate Concerns)
⚠️ Malicious official image (compromised docker.io account)  
⚠️ Supply chain attack in dependency (not StePanel's job)  
⚠️ Vulnerable base image (image scanning separate tool)  

## Attack Scenarios Prevented

| Scenario | Prevention |
|----------|-----------|
| Malicious private registry | Allowlist blocks all private registries |
| Typosquatting registry | Whitelisted registries only |
| Localhost attack | localhost:5000 not in allowlist |
| Registry enumeration | Admin controls allowlist explicitly |
| Large image DoS | Size limits prevent disk fill |

## Phased Implementation

### v0.7.0 (Blocker Fix)
- [x] ParseContainerImage() validation
- [x] Registry allowlist enforcement
- [x] Image size limits
- [ ] Site-specific overrides (deferred)
- [ ] Integration with Dockerfile parsing

### v0.8.0
- [ ] Database schema for site-specific overrides
- [ ] Web UI for allowlist management
- [ ] Per-site image size configuration
- [ ] Audit logging for blocked images

### v0.9.0+
- [ ] Image hash verification (require SHA256)
- [ ] Signature verification (cosign/Notary)
- [ ] Vulnerability scanning integration
- [ ] Supply chain attestation (SLSA provenance)

## Testing

```go
func TestParseContainerImage(t *testing.T) {
	tests := []struct {
		image    string
		valid    bool
		expected *ContainerImageRef
	}{
		{"alpine", true, &ContainerImageRef{"docker.io", "library", "alpine", "latest", ""}},
		{"ubuntu:20.04", true, &ContainerImageRef{"docker.io", "library", "ubuntu", "20.04", ""}},
		{"ghcr.io/myorg/myapp:v1.0", true, &ContainerImageRef{"ghcr.io", "myorg", "myapp", "v1.0", ""}},
		{"quay.io/coreos/etcd:v3.5.0", true, &ContainerImageRef{"quay.io", "coreos", "etcd", "v3.5.0", ""}},
		// Blocked
		{"attacker.com/malware:latest", false, nil},
		{"registry.internal.example.com/image:tag", false, nil},
		{"localhost:5000/local/image", false, nil},
		{"127.0.0.1:5000/image", false, nil},
	}

	for _, tt := range tests {
		ref, err := ParseContainerImage(tt.image, []string{"docker.io", "ghcr.io", "quay.io"})
		if (err == nil) != tt.valid {
			t.Fatalf("%s: valid=%v, got error=%v", tt.image, tt.valid, err)
		}
		if tt.valid && ref.Registry != tt.expected.Registry {
			t.Fatalf("%s: registry=%s, expected %s", tt.image, ref.Registry, tt.expected.Registry)
		}
	}
}
```

## References

- OCI Image Spec: https://github.com/opencontainers/image-spec
- Docker Image Specification: https://docs.docker.com/engine/reference/commandline/pull/
- CNCF Supply Chain Security: https://www.cncf.io/projects/supply-chain-security/
- SLSA Provenance: https://slsa.dev/
