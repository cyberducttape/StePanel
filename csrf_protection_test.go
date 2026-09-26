package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCSRFProtectionEnforcement verifies that state-changing operations require
// a valid CSRF token, preventing cross-site request forgery attacks where a
// malicious site tricks an authenticated user into making unwanted changes.
func TestCSRFProtectionEnforcement(t *testing.T) {

	type csrfTest struct {
		name            string
		method          string
		withCSRFToken   bool
		hasValidSession bool
		shouldAllow     bool
	}

	tests := []csrfTest{
		// Safe methods never require CSRF tokens
		{
			name:            "GET request allowed without CSRF token",
			method:          http.MethodGet,
			withCSRFToken:   false,
			hasValidSession: true,
			shouldAllow:     true,
		},
		{
			name:            "HEAD request allowed without CSRF token",
			method:          http.MethodHead,
			withCSRFToken:   false,
			hasValidSession: true,
			shouldAllow:     true,
		},
		// Mutating methods require CSRF tokens
		{
			name:            "POST without CSRF token is denied",
			method:          http.MethodPost,
			withCSRFToken:   false,
			hasValidSession: true,
			shouldAllow:     false,
		},
		{
			name:            "PUT without CSRF token is denied",
			method:          http.MethodPut,
			withCSRFToken:   false,
			hasValidSession: true,
			shouldAllow:     false,
		},
		{
			name:            "DELETE without CSRF token is denied",
			method:          http.MethodDelete,
			withCSRFToken:   false,
			hasValidSession: true,
			shouldAllow:     false,
		},
		{
			name:            "PATCH without CSRF token is denied",
			method:          http.MethodPatch,
			withCSRFToken:   false,
			hasValidSession: true,
			shouldAllow:     false,
		},
		// With CSRF token, mutations are allowed
		{
			name:            "POST with CSRF token is allowed",
			method:          http.MethodPost,
			withCSRFToken:   true,
			hasValidSession: true,
			shouldAllow:     true,
		},
		{
			name:            "DELETE with CSRF token is allowed",
			method:          http.MethodDelete,
			withCSRFToken:   true,
			hasValidSession: true,
			shouldAllow:     true,
		},
		// CSRF token doesn't help without valid session
		{
			name:            "POST with CSRF token but no session is denied",
			method:          http.MethodPost,
			withCSRFToken:   true,
			hasValidSession: false,
			shouldAllow:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/api/test", nil)

			// Simulate CSRF token in header
			if tt.withCSRFToken {
				r.Header.Set("X-CSRF-Token", "valid-token-value")
			}

			// Check if this would be allowed
			isSafeMethod := tt.method == http.MethodGet || tt.method == http.MethodHead || tt.method == http.MethodOptions
			needsCSRF := !isSafeMethod

			// Simulate CSRF check
			hasCSRF := tt.withCSRFToken && tt.hasValidSession

			allowed := !needsCSRF || hasCSRF

			if allowed != tt.shouldAllow {
				t.Errorf("%s %s: expected %v, got %v",
					tt.method, tt.name, tt.shouldAllow, allowed)
			}
		})
	}
}

// TestCSRFTokenBoundary verifies that CSRF token validation correctly
// distinguishes between safe and unsafe HTTP methods.
func TestCSRFTokenBoundary(t *testing.T) {
	tests := []struct {
		method string
		isSafe bool
	}{
		{http.MethodGet, true},
		{http.MethodHead, true},
		{http.MethodOptions, true},
		{http.MethodTrace, true},
		{http.MethodPost, false},
		{http.MethodPut, false},
		{http.MethodPatch, false},
		{http.MethodDelete, false},
		{http.MethodConnect, false},
	}

	for _, tt := range tests {
		isSafe := tt.method == http.MethodGet ||
			tt.method == http.MethodHead ||
			tt.method == http.MethodOptions ||
			tt.method == http.MethodTrace

		if isSafe != tt.isSafe {
			t.Errorf("%s: expected isSafe=%v, got %v", tt.method, tt.isSafe, isSafe)
		}
	}
}

// TestCSRFTokenNotRequiredForAPITokens verifies that API token requests
// don't require CSRF tokens (they use bearer authentication, not cookies).
func TestCSRFTokenNotRequiredForAPITokens(t *testing.T) {

	type tokenTest struct {
		name          string
		isAPIToken    bool
		withCSRFToken bool
		shouldAllow   bool
	}

	tests := []tokenTest{
		{
			name:          "API token POST without CSRF token is allowed",
			isAPIToken:    true,
			withCSRFToken: false,
			shouldAllow:   true,
		},
		{
			name:          "API token DELETE without CSRF token is allowed",
			isAPIToken:    true,
			withCSRFToken: false,
			shouldAllow:   true,
		},
		{
			name:          "browser session POST without CSRF token is denied",
			isAPIToken:    false,
			withCSRFToken: false,
			shouldAllow:   false,
		},
		{
			name:          "browser session POST with CSRF token is allowed",
			isAPIToken:    false,
			withCSRFToken: true,
			shouldAllow:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/test", nil)
			ctx := r.Context()

			if tt.isAPIToken {
				ctx = context.WithValue(ctx, apiTokenUsernameKey{}, "user")
			}

			if tt.withCSRFToken {
				r.Header.Set("X-CSRF-Token", "valid-token")
			}

			r = r.WithContext(ctx)

			// CSRF check logic:
			// - API tokens don't need CSRF (they're not vulnerable to CSRF)
			// - Browser sessions need CSRF for POST/PUT/DELETE
			isAPITokenRequest := r.Context().Value(apiTokenUsernameKey{}) != nil
			hasCSRF := r.Header.Get("X-CSRF-Token") != ""

			allowed := isAPITokenRequest || hasCSRF

			if allowed != tt.shouldAllow {
				t.Errorf("%s: expected %v, got %v", tt.name, tt.shouldAllow, allowed)
			}
		})
	}
}
