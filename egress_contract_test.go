package stepanel

import (
	"os"
	"strings"
	"testing"
)

// Tenant code (site PHP, Node and Python apps, scheduled tasks) must not reach
// private networks or cloud instance metadata, which can hand out host
// credentials to a compromised plugin or an SSRF bug. Every unit that runs
// tenant code carries the tenant egress policy.
func TestTenantCodeUnitsCarryTheEgressPolicy(t *testing.T) {
	installer, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(string(installer), "\n") {
		if strings.Contains(line, `fpm-socket-root.conf"`) && strings.Contains(line, "printf '[Service]") {
			found = true
			if !strings.Contains(line, `"$TENANT_EGRESS_LINES"`) {
				t.Errorf("the PHP-FPM drop-in must carry the tenant egress policy: %s", line)
			}
		}
	}
	if !found {
		t.Fatal("the PHP-FPM drop-in was not found in install.sh")
	}
	policy, err := os.ReadFile("deploy/lib/install-tenant-egress.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "100.100.100.200/32", "fc00::/7", "fe80::/10"} {
		if !strings.Contains(string(policy), denied) {
			t.Errorf("the tenant egress policy does not deny %s", denied)
		}
	}
	appctl, err := os.ReadFile("deploy/integrations/stepanel-appctl")
	if err != nil {
		t.Fatal(err)
	}
	units := strings.Split(string(appctl), "[Service]")[1:]
	if len(units) < 4 {
		t.Fatalf("expected the Node, generic, Python and task unit templates, found %d", len(units))
	}
	for i, unit := range units {
		if end := strings.Index(unit, "\nEOF"); end >= 0 {
			unit = unit[:end]
		}
		if !strings.Contains(unit, "\n$tenant_egress\n") {
			t.Errorf("appctl unit template %d does not carry the tenant egress policy:\n%s", i+1, unit)
		}
	}
	if !strings.Contains(string(appctl), "printf '%s\\n' 'IPAddressDeny=169.254.0.0/16 100.100.100.200/32 fd00:ec2::254/128'") {
		t.Error("appctl must fall back to denying cloud metadata when no policy file exists")
	}
}
