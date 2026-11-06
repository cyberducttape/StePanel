package stepanel

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// An immediate transaction that reads then writes waits for another
// process's writer instead of failing when that writer commits.
func TestImmediateTransactionWaitsForAConcurrentWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	other, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	store := NewWebhookConfigStore(db)
	if err := store.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWebhookConfig("shop", webhookSecretA, []string{webhookRepo}, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE webhook_configs SET updated_at = CURRENT_TIMESTAMP WHERE site = 'shop'`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- store.RevokeWebhookConfig("shop") }()
	time.Sleep(200 * time.Millisecond)
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("revocation during a concurrent write = %v", err)
	}
	if config, err := store.GetWebhookConfig("shop"); err != nil || config != nil {
		t.Fatalf("webhook after revocation = %+v, %v", config, err)
	}
	// The pool is still usable afterwards.
	if err := store.SetWebhookConfig("shop", webhookSecretB, []string{webhookRepo}, []string{"main"}); err != nil {
		t.Fatal(err)
	}
}
