package main

import "testing"

func TestCommandPath(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{name: "production doctor", args: []string{"doctor", "--production"}, want: "/api/admin/production-readiness", ok: true},
		{name: "doctor defaults to production report", args: []string{"doctor"}, want: "/api/admin/production-readiness", ok: true},
		{name: "job watch", args: []string{"jobs", "watch", "site-create-1"}, want: "/api/jobs/site-create-1", ok: true},
		{name: "site inspect", args: []string{"site", "inspect", "example"}, want: "/api/sites/overview/example", ok: true},
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
