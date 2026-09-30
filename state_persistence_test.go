package main

import (
	"errors"
	"testing"
)

func TestPersistMapKeyChangePreservesReloadedPeerSnapshot(t *testing.T) {
	values := map[string]string{"local": "before"}
	next := "requested"
	err := persistMapKeyChange(values, "local", &next, func() error {
		values = map[string]string{"peer": "newer"}
		return errors.Join(errControlPlaneStateRefreshed, errors.New("CAS retries exhausted"))
	})
	if err == nil {
		t.Fatal("expected persistence error")
	}
	if values["peer"] != "newer" || values["local"] != "" {
		t.Fatalf("stale rollback overwrote reloaded peer snapshot: %#v", values)
	}
}

func TestPersistMapKeyChangeRollsBackOnOrdinaryWriteFailure(t *testing.T) {
	values := map[string]string{"local": "before"}
	next := "requested"
	err := persistMapKeyChange(values, "local", &next, func() error {
		return errors.New("disk write failed")
	})
	if err == nil {
		t.Fatal("expected persistence error")
	}
	if values["local"] != "before" {
		t.Fatalf("failed write left local mutation behind: %#v", values)
	}
}
