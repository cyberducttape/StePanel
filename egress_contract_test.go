package stepanel

import (
	"os"
	"strings"
	"testing"
)

// Tenant code (site PHP, Node and Python apps, scheduled tasks) must not reach
// cloud instance metadata, which can hand out host credentials to a
// compromised plugin or an SSRF bug. Every unit that runs tenant code denies
// the metadata endpoints.
func TestTenantCodeUnitsDenyCloudMetadata(t *testing.T) {
	const awsIPv6 = "fd00:ec2::254/128"
	installer, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(installer), "\n") {
		if strings.Contains(line, `fpm-socket-root.conf"`) && strings.Contains(line, "printf '[Service]") {
			if !strings.Contains(line, "IPAddressDeny=169.254.169.254/32") || !strings.Contains(line, awsIPv6) {
				t.Errorf("the PHP-FPM drop-in must deny cloud metadata: %s", line)
			}
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
		end := strings.Index(unit, "\nEOF")
		if end < 0 {
			end = len(unit)
		}
		unit = unit[:end]
		if !strings.Contains(unit, "\nIPAddressDeny=") || !strings.Contains(unit, "169.254.") || !strings.Contains(unit, awsIPv6) {
			t.Errorf("appctl unit template %d does not deny cloud metadata:\n%s", i+1, unit)
		}
	}
}
