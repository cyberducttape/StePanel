package main

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestPrivilegeEscalationPrevention verifies that customers cannot escalate to
// administrator privileges. This is a critical security boundary: only the
// configured admin username can perform admin operations.
func TestPrivilegeEscalationPrevention(t *testing.T) {
	auth := Auth{Username: "admin"}

	type escalationTest struct {
		name        string
		username    string
		shouldAllow bool
	}

	tests := []escalationTest{
		{
			name:        "customer API token cannot be admin",
			username:    "customer",
			shouldAllow: false,
		},
		{
			name:        "different user cannot be admin",
			username:    "alice",
			shouldAllow: false,
		},
		{
			name:        "empty username cannot be admin",
			username:    "",
			shouldAllow: false,
		},
		{
			name:        "only admin username can be admin",
			username:    "admin",
			shouldAllow: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/admin/test", nil)
			ctx := r.Context()

			if tt.username != "" {
				ctx = context.WithValue(ctx, apiTokenUsernameKey{}, tt.username)
			}

			r = r.WithContext(ctx)

			// Check if they can perform admin operations
			isAdmin := auth.IsAdministrator(r)

			if isAdmin != tt.shouldAllow {
				t.Errorf("IsAdministrator for user %q: expected %v, got %v",
					tt.username, tt.shouldAllow, isAdmin)
			}
		})
	}
}

// TestNoImplicitAdminEscalation verifies that a customer cannot become an admin
// by any request-level manipulation. Only the configured admin username is recognized.
func TestNoImplicitAdminEscalation(t *testing.T) {
	auth := Auth{Username: "operator"}

	tests := []struct {
		name       string
		username   string
		shouldPass bool
	}{
		{
			name:       "API token with wrong name cannot grant admin access",
			username:   "admin",
			shouldPass: false,
		},
		{
			name:       "only configured admin username grants access",
			username:   "operator",
			shouldPass: true,
		},
		{
			name:       "no authentication has no admin access",
			username:   "",
			shouldPass: false,
		},
		{
			name:       "a different admin name doesn't work",
			username:   "root",
			shouldPass: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/admin/test", nil)
			ctx := r.Context()

			if tt.username != "" {
				ctx = context.WithValue(ctx, apiTokenUsernameKey{}, tt.username)
			}

			r = r.WithContext(ctx)

			isAdmin := auth.IsAdministrator(r)
			if isAdmin != tt.shouldPass {
				t.Errorf("IsAdministrator for user %q: expected %v, got %v",
					tt.username, tt.shouldPass, isAdmin)
			}
		})
	}
}
