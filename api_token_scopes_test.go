package main

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestAPITokenScopeEnforcement verifies that API tokens with limited scopes
// cannot perform operations outside their authorized scope. This is a critical
// security boundary: a token with only "site:read" should not be able to
// create backups or deploy code.
func TestAPITokenScopeEnforcement(t *testing.T) {
	auth := Auth{Username: "admin"}

	type scopeTest struct {
		name           string
		scopes         []string
		isAPIToken     bool
		requiredScope  string
		shouldAllow    bool
	}

	tests := []scopeTest{
		{
			name:           "site:read token can access site:read operations",
			scopes:         []string{"site:read"},
			isAPIToken:     true,
			requiredScope:  "site:read",
			shouldAllow:    true,
		},
		{
			name:           "site:read token CANNOT deploy (deploy:write required)",
			scopes:         []string{"site:read"},
			isAPIToken:     true,
			requiredScope:  "deploy:write",
			shouldAllow:    false,
		},
		{
			name:           "site:read token CANNOT create backup (backup:create required)",
			scopes:         []string{"site:read"},
			isAPIToken:     true,
			requiredScope:  "backup:create",
			shouldAllow:    false,
		},
		{
			name:           "deploy:write token can deploy",
			scopes:         []string{"deploy:write"},
			isAPIToken:     true,
			requiredScope:  "deploy:write",
			shouldAllow:    true,
		},
		{
			name:           "deploy:write token CANNOT create backup (backup:create required)",
			scopes:         []string{"deploy:write"},
			isAPIToken:     true,
			requiredScope:  "backup:create",
			shouldAllow:    false,
		},
		{
			name:           "multi-scope token (site:read, deploy:write, backup:read)",
			scopes:         []string{"site:read", "deploy:write", "backup:read"},
			isAPIToken:     true,
			requiredScope:  "backup:read",
			shouldAllow:    true,
		},
		{
			name:           "multi-scope token CANNOT perform unauthorized action (ssh:write)",
			scopes:         []string{"site:read", "deploy:write", "backup:read"},
			isAPIToken:     true,
			requiredScope:  "ssh:write",
			shouldAllow:    false,
		},
		{
			name:           "browser session (not API token) ignores scopes",
			scopes:         []string{}, // Empty scopes shouldn't matter
			isAPIToken:     false,
			requiredScope:  "deploy:write",
			shouldAllow:    true, // Browser sessions always allowed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/test", nil)
			ctx := r.Context()

			if tt.isAPIToken {
				ctx = context.WithValue(ctx, apiTokenUsernameKey{}, "alice")
			}
			ctx = context.WithValue(ctx, apiTokenScopesKey{}, tt.scopes)
			r = r.WithContext(ctx)

			allowed := auth.HasRequiredCustomerScope(r, tt.requiredScope)
			if allowed != tt.shouldAllow {
				t.Errorf("scope %q: expected %v, got %v", tt.requiredScope, tt.shouldAllow, allowed)
			}
		})
	}
}

// TestLegacyTokensRetainFullAccess verifies that tokens created before scope
// enforcement are backward-compatible: they retain full access across all scopes.
// This is important for backward compatibility during the transition period.
func TestLegacyTokensRetainFullAccess(t *testing.T) {
	auth := Auth{Username: "admin"}

	// Create a request context with empty scopes (legacy token)
	// This simulates a token that was created before the scopes feature existed
	r := httptest.NewRequest("GET", "/api/test", nil)
	ctx := r.Context()
	ctx = context.WithValue(ctx, apiTokenUsernameKey{}, "alice")
	ctx = context.WithValue(ctx, apiTokenScopesKey{}, []string{}) // Empty scopes = legacy
	r = r.WithContext(ctx)

	// Verify that HasRequiredCustomerScope returns true for any scope (backward compat)
	scopes := []string{"site:read", "deploy:write", "backup:create"}
	for _, scope := range scopes {
		if !auth.HasRequiredCustomerScope(r, scope) {
			t.Errorf("legacy token should have access to %s", scope)
		}
	}
}
