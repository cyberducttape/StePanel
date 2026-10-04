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
