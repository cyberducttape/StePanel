package stepanel

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

func TestPersistMapFilterPreservesReloadedPeerSnapshot(t *testing.T) {
	values := map[string]string{"site-a": "owned", "site-b": "keep"}
	err := persistMapFilter(values, func(key string, _ string) bool {
		return key == "site-a"
	}, func() error {
		values = map[string]string{"site-a": "peer-new", "site-c": "peer-added"}
		return errors.Join(errControlPlaneStateRefreshed, errors.New("CAS retries exhausted"))
	})
	if err == nil {
		t.Fatal("expected persistence error")
	}
	if values["site-a"] != "peer-new" || values["site-c"] != "peer-added" {
		t.Fatalf("stale filter rollback overwrote the refreshed peer snapshot: %#v", values)
	}
}

func TestPersistMapFilterRollsBackOnOrdinaryWriteFailure(t *testing.T) {
	values := map[string]string{"site-a": "owned", "site-b": "keep"}
	err := persistMapFilter(values, func(key string, _ string) bool {
		return key == "site-a"
	}, func() error { return errors.New("disk write failed") })
	if err == nil {
		t.Fatal("expected persistence error")
	}
	if values["site-a"] != "owned" || values["site-b"] != "keep" {
		t.Fatalf("failed filter persistence lost prior values: %#v", values)
	}
}

func TestRetryRefreshedStateChangeReappliesAfterAPeerWrite(t *testing.T) {
	calls := 0
	err := retryRefreshedStateChange(func() error {
		calls++
		if calls == 1 {
			return errors.Join(errControlPlaneStateRefreshed, errors.New("conflict at key demo"))
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("retry = %v after %d calls, want success on the second", err, calls)
	}
}

func TestRetryRefreshedStateChangeStopsOnOtherErrorsAndPersistentConflicts(t *testing.T) {
	calls := 0
	disk := errors.New("disk write failed")
	if err := retryRefreshedStateChange(func() error { calls++; return disk }); !errors.Is(err, disk) || calls != 1 {
		t.Fatalf("ordinary failure: %v after %d calls", err, calls)
	}
	calls = 0
	err := retryRefreshedStateChange(func() error { calls++; return errControlPlaneStateRefreshed })
	if !errors.Is(err, errControlPlaneStateRefreshed) || calls != refreshedStateChangeAttempts {
		t.Fatalf("persistent conflict: %v after %d calls", err, calls)
	}
}
