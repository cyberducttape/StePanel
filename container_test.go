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
			_, err := ValidateContainerImageForSite("testsite", tt.image)
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
