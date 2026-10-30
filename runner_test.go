package stepanel

import (
	"context"
	"testing"
)

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

func TestDecodeBuildRequestRejectsMalformedAndUnsafeCommands(t *testing.T) {
	image := "ghcr.io/example/build@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	valid := []byte(`{"site":"shop","image":"` + image + `","commands":["make build"]}`)
	if _, err := decodeBuildRequest(valid); err != nil {
		t.Fatalf("valid immutable build request rejected: %v", err)
	}
	for _, body := range [][]byte{
		[]byte(`{"site":"shop","image":"` + image + `","commands":["make build"],"unexpected":true}`),
		[]byte(`{"site":"shop","image":"` + image + `","commands":["make build"]} {}`),
		[]byte(`{"site":"shop","image":"` + image + `","commands":["make build\nrm -rf /"]}`),
		[]byte(`{"site":"shop","image":"` + image + `","commands":[]}`),
	} {
		if _, err := decodeBuildRequest(body); err == nil {
			t.Errorf("unsafe or malformed build request accepted: %s", body)
		}
	}
}

func TestRunnerBuildUsesTypedDurableExecutor(t *testing.T) {
	route, ok := siteOperationRoutes["runner.build"]
	if !ok || route.validate == nil || route.execute == nil {
		t.Fatal("runner build must register typed admission validation and worker execution")
	}
	image := "ghcr.io/example/build@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := []byte(`{"site":"shop","image":"` + image + `","commands":["make build"]}`)
	if err := route.validate(body); err != nil {
		t.Fatalf("valid runner command rejected: %v", err)
	}
	if _, err := route.execute(&App{}, context.Background(), siteOperationCommand{Path: "/api/runner/build", Site: "shop", Body: body}, siteOperationPrincipal{}); err == nil {
		t.Fatal("worker execution without current site authorization unexpectedly succeeded")
	}
}
