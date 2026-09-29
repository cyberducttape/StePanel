package state

import (
	"errors"
	"strings"
	"testing"
)

func TestStateErrorConstructorsAndFormatting(t *testing.T) {
	underlying := errors.New("disk full")
	cases := []struct {
		name          string
		make          func() StateError
		category      ErrorCategory
		requiresAudit bool
	}{
		{"persistence", func() StateError { return NewPersistenceError("save", underlying, "state") }, Persistence, true},
		{"corruption", func() StateError { return NewCorruptionError("load", underlying, "state") }, Corruption, true},
		{"temporary", func() StateError { return NewTemporaryError("retry", underlying, "state") }, Temporary, false},
		{"cleanup", func() StateError { return NewCleanupError("remove", underlying, "state") }, Cleanup, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.make()
			if err.Category != tc.category || err.RequiresAudit != tc.requiresAudit || !errors.Is(err.Err, underlying) {
				t.Fatalf("state error = %#v", err)
			}
			if got := err.Error(); !strings.Contains(got, string(tc.category)) || !strings.Contains(got, "disk full") {
				t.Fatalf("formatted state error = %q", got)
			}
		})
	}
}

func TestStateErrorHandleCoversOperationalCategories(t *testing.T) {
	for _, category := range []ErrorCategory{Persistence, Corruption, Temporary, Cleanup, Unsupported, "unknown"} {
		t.Run(string(category), func(t *testing.T) {
			StateError{Category: category, Operation: "test", Err: errors.New("failure"), Message: "details"}.Handle()
		})
	}
}
