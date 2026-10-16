package stepanel

import (
	"reflect"
	"testing"
)

func TestConfiguredServiceNamesUsesSelectedStack(t *testing.T) {
	status := map[string]string{
		"caddy":      "active",
		"postgresql": "active",
		"php-fpm":    "active",
		"fail2ban":   "active",
	}
	got := configuredServiceNames(Config{WebServer: "caddy", DBEngine: "postgresql"}, status)
	want := []string{"caddy", "postgresql", "php-fpm", "fail2ban"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configured services = %#v, want %#v", got, want)
	}
}

func TestConfiguredServiceNamesOmitsUninstalledOptionalServices(t *testing.T) {
	got := configuredServiceNames(Config{WebServer: "apache", DBEngine: "mariadb"}, map[string]string{})
	want := []string{"apache2", "mariadb", "php-fpm"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configured services = %#v, want %#v", got, want)
	}
}

func TestResolvedServiceStatusUsesDistributionAliases(t *testing.T) {
	status := map[string]string{"httpd": "active", "mariadb": "active", "exim4": "inactive"}
	for name, want := range map[string]string{"apache2": "active", "mysql": "active", "exim": "inactive"} {
		if got := resolvedServiceStatus(name, status); got != want {
			t.Fatalf("status for %s = %q, want %q", name, got, want)
		}
	}
}

func TestFail2banHasEnabledJail(t *testing.T) {
	for _, test := range []struct {
		output string
		want   bool
	}{
		{"Status\n|- Number of jail: 0\n`- Jail list:\t\n", false},
		{"Status\n|- Number of jail: 2\n`- Jail list:\tsshd, apache-auth\n", true},
		{"Jail list:\t, , \n", false},
	} {
		if got := fail2banHasEnabledJail(test.output); got != test.want {
			t.Errorf("fail2banHasEnabledJail(%q) = %t, want %t", test.output, got, test.want)
		}
	}
}
