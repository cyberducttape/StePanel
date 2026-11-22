package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/sdk/go/stepanel"
)

func TestCommandPath(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{name: "production doctor", args: []string{"doctor", "--production"}, want: "/api/v1/admin/production-readiness", ok: true},
		{name: "doctor defaults to production report", args: []string{"doctor"}, want: "/api/v1/admin/production-readiness", ok: true},
		{name: "doctor json output", args: []string{"doctor", "--json"}, want: "/api/v1/admin/production-readiness", ok: true},
		{name: "job watch", args: []string{"jobs", "watch", "site-create-1"}, want: "/api/v1/jobs/site-create-1", ok: true},
		{name: "site inspect", args: []string{"site", "inspect", "example"}, want: "/api/v1/sites/overview/example", ok: true},
		{name: "unknown mutation", args: []string{"site", "delete", "example"}, ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := commandPath(test.args)
			if ok != test.ok || got != test.want {
				t.Fatalf("commandPath(%v) = %q, %v; want %q, %v", test.args, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestRenderReadinessReportExplainsFailuresAndNextSteps(t *testing.T) {
	var output bytes.Buffer
	report := readinessReport{
		IsProduction:  true,
		OverallStatus: "critical",
		Checks: []readinessCheck{
			{Name: "Root Broker", Status: "fail", Message: "broker socket is unavailable", Remediation: "Start the configured broker and verify its socket"},
			{Name: "Backup Storage", Status: "warning", Message: "remote restore has not been verified"},
		},
	}
	if err := renderReadinessReport(&output, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"StePanel readiness: CRITICAL (production mode)",
		"[FAIL] Root Broker",
		"broker socket is unavailable",
		"Next: Start the configured broker and verify its socket",
		"[WARNING] Backup Storage",
		"remote restore has not been verified",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("doctor output missing %q:\n%s", want, output.String())
		}
	}
}

func TestReadinessReportFailureExitPolicy(t *testing.T) {
	for _, test := range []struct {
		status string
		failed bool
	}{
		{status: "healthy", failed: false},
		{status: "degraded", failed: false},
		{status: "critical", failed: true},
		{status: "not_ready", failed: true},
		{status: "unknown", failed: true},
		{status: "", failed: true},
	} {
		if got := readinessReportFailed(readinessReport{OverallStatus: test.status}); got != test.failed {
			t.Errorf("status %q: failed = %v, want %v", test.status, got, test.failed)
		}
	}
}

func TestDoctorShowsReadableAndJSONReportsAndFailsNotReady(t *testing.T) {
	const body = `{"is_production":true,"overall_status":"critical","checks":[{"name":"Root Broker","status":"fail","severity":"critical","message":"socket is unavailable","remediation":"Start the broker"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/production-readiness" {
			t.Errorf("doctor request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "human", want: "[FAIL] Root Broker"},
		{name: "json", args: []string{"--json"}, want: `"overall_status":"critical"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output, errorsOut bytes.Buffer
			err := doctor(stepanel.New(server.URL, "test-token"), context.Background(), test.args, &output, &errorsOut)
			if err == nil || !strings.Contains(err.Error(), "readiness is critical") {
				t.Fatalf("doctor error = %v, want critical readiness", err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("doctor output missing %q:\n%s", test.want, output.String())
			}
		})
	}
}
