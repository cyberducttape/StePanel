package stepanel

import (
	"context"
	"testing"
)

func TestDecodeComposerInstall(t *testing.T) {
	got, err := decodeComposerInstall([]byte(`{"development":true,"optimize_autoloader":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Development || !got.OptimizeAutoloader {
		t.Fatalf("decoded request = %+v", got)
	}
	for _, body := range []string{
		`{"development":true,"unexpected":true}`,
		`{"development":true} {}`,
		`{"development":true`,
	} {
		if _, err := decodeComposerInstall([]byte(body)); err == nil {
			t.Errorf("decodeComposerInstall(%q) accepted malformed or unknown data", body)
		}
	}
	if err := validateComposerInstallPayload([]byte(`{}`)); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}

func TestComposerInstallUsesTypedDurableExecutor(t *testing.T) {
	route, ok := siteOperationRoutes["composer.install"]
	if !ok {
		t.Fatal("Composer durable operation is not registered")
	}
	if route.execute == nil || route.validate == nil {
		t.Fatal("Composer durable operation must validate and execute a typed command")
	}
	if err := route.validate([]byte(`{"development":false}`)); err != nil {
		t.Fatalf("valid durable command rejected: %v", err)
	}
	if _, err := route.execute(&App{}, context.Background(), siteOperationCommand{
		Operation: "composer.install", Path: "/api/composer/shop/install", Site: "shop",
		Body: []byte(`{"development":false}`),
	}, siteOperationPrincipal{}); err == nil {
		t.Fatal("worker execution without a current authorized principal unexpectedly succeeded")
	}
}
