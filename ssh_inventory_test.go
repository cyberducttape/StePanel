package stepanel

import "testing"

func TestConfiguredSSHServers(t *testing.T) {
	t.Setenv("STEPANEL_SSH_SERVERS", "prod-a, prod-a,admin@example.com:2222, bad host")
	servers := configuredSSHServers()
	if len(servers) != 2 || servers[0] != "prod-a" || servers[1] != "admin@example.com:2222" {
		t.Fatalf("servers = %#v", servers)
	}
	if err := validateSSHServers(); err == nil {
		t.Fatal("invalid SSH alias accepted")
	}
}
