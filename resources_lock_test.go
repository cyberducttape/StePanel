package main

import "testing"

func TestResourceMutationLockKeysIncludeOwningAccount(t *testing.T) {
	keys := resourceMutationLockKeys("site-one", "customer")
	if len(keys) != 2 || keys[0] != "site-one" || keys[1] != "account:customer" {
		t.Fatalf("resource mutation keys = %#v, want site and account keys", keys)
	}
	if keys := resourceMutationLockKeys("site-one", ""); len(keys) != 1 || keys[0] != "site-one" {
		t.Fatalf("unowned resource mutation keys = %#v, want site key only", keys)
	}
}
