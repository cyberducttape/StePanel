package stepanel

import "testing"

func TestRunnerImagePatternRequiresImmutableDigest(t *testing.T) {
	valid := "ghcr.io/example/build@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if !runnerImagePattern.MatchString(valid) {
		t.Fatalf("immutable image reference was rejected: %q", valid)
	}
	for _, image := range []string{
		"ghcr.io/example/build:latest",
		"ghcr.io/example/build:v1",
		"ghcr.io/example/build@sha256:0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef",
		"ghcr.io/example/build@sha256:short",
	} {
		if runnerImagePattern.MatchString(image) {
			t.Errorf("mutable or invalid image reference was accepted: %q", image)
		}
	}
}
