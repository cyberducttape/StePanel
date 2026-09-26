package rootbroker

import (
	"testing"
)

func TestValidateSiteName(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		site    string
		wantErr bool
	}{
		{"valid lowercase", "mysite", false},
		{"valid with dash", "my-site", false},
		{"valid with underscore", "my_site", false},
		{"valid mixed", "site-name_123", false},
		{"empty", "", true},
		{"too long", "a" + string(make([]byte, 32)), true},
		{"uppercase not allowed", "MySite", true},
		{"special chars not allowed", "site@home", true},
		{"space not allowed", "my site", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateSiteName(tt.site)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSiteName(%q) error = %v, wantErr %v", tt.site, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDomain(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		domain  string
		wantErr bool
	}{
		{"valid domain", "example.com", false},
		{"valid subdomain", "www.example.com", false},
		{"valid long domain", "very.long.subdomain.example.org", false},
		{"empty", "", true},
		{"no dot", "localhost", true},
		{"leading dash", "-example.com", true},
		{"trailing dash", "example.com-", true},
		{"too long", "a." + string(make([]byte, 300)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateDomain(tt.domain)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDomain(%q) error = %v, wantErr %v", tt.domain, err, tt.wantErr)
			}
		})
	}
}

func TestValidatePort(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{"valid low port", 1024, false},
		{"valid high port", 65535, false},
		{"valid mid port", 8080, false},
		{"too low", 1023, true},
		{"negative", -1, true},
		{"too high", 65536, true},
		{"zero", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidatePort(tt.port)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePort(%d) error = %v, wantErr %v", tt.port, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSSHKey(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid ed25519", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG1wI", false},
		{"valid ecdsa256", "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAI", false},
		{"valid ecdsa384", "ecdsa-sha2-nistp384 AAAAE2VjZHNhLXNoYTItbmlzdHAzODQAAAAI", false},
		{"valid rsa", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAB", false},
		{"with comment", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG1wI user@host", false},
		{"empty", "", true},
		{"too long", "ssh-ed25519 " + string(make([]byte, 9000)), true},
		{"cr character", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG1wI\r", true},
		{"invalid format", "not-a-key", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateSSHKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSSHKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestValidateBcryptHash(t *testing.T) {
	v := NewValidator("/var/www")

	// Valid bcrypt hash (exactly 60 chars, starts with $2a$, $2b$, or $2y$)
	validHash := "$2b$12$8qkqvzKeDXmNyWd8KKAkCeVHVWKrq47aT8DwqMKtgTUzWv0Em5eSm"

	tests := []struct {
		name    string
		hash    string
		wantErr bool
	}{
		{"valid $2a$", "$2a$12$8qkqvzKeDXmNyWd8KKAkCeVHVWKrq47aT8DwqMKtgTUzWv0Em5eSm", false},
		{"valid $2b$", validHash, false},
		{"empty", "", true},
		{"too short", "$2b$12$short", true},
		{"too long", validHash + "extra", true},
		{"wrong prefix", "$1$12$8qkqvzKeDXmNyWd8KKAkCeVHVWKrq47aT8DwqMKtgTUzWv0Em5eSm", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateBcryptHash(tt.hash)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBcryptHash(%q) error = %v, wantErr %v", tt.hash, err, tt.wantErr)
			}
		})
	}
}

func TestValidateUsername(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		user    string
		wantErr bool
	}{
		{"valid", "user123", false},
		{"with dash", "user-name", false},
		{"with underscore", "user_name", false},
		{"empty", "", true},
		{"too long", "a" + string(make([]byte, 32)), true},
		{"uppercase", "UserName", true},
		{"special chars", "user@host", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateUsername(tt.user)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateUsername(%q) error = %v, wantErr %v", tt.user, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDatabaseName(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		db      string
		wantErr bool
	}{
		{"valid", "mydb", false},
		{"with underscore", "my_db", false},
		{"with numbers", "db123", false},
		{"empty", "", true},
		{"too long", "a" + string(make([]byte, 64)), true},
		{"with dash", "my-db", true},
		{"uppercase", "MyDB", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateDatabaseName(tt.db)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDatabaseName(%q) error = %v, wantErr %v", tt.db, err, tt.wantErr)
			}
		})
	}
}

func TestValidateGitRepository(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		repo    string
		wantErr bool
	}{
		{"valid ssh", "git@github.com:user/repo.git", false},
		{"valid https", "https://github.com/user/repo.git", false},
		{"valid ssh with path", "ssh://git@github.com/user/repo.git", false},
		{"empty", "", true},
		{"no protocol", "github.com:user/repo", true},
		{"with traversal", "git@github.com:../../../etc/passwd", true},
		{"with newline", "git@github.com:user/repo\n", true},
		{"too long", "https://" + string(make([]byte, 2100)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateGitRepository(tt.repo)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateGitRepository(%q) error = %v, wantErr %v", tt.repo, err, tt.wantErr)
			}
		})
	}
}

func TestValidateGitRef(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{"valid branch", "main", false},
		{"valid tag", "v1.0.0", false},
		{"valid commit", "abc123def456", false},
		{"with slash", "release/1.0", false},
		{"empty", "", true},
		{"too long", "a" + string(make([]byte, 256)), true},
		{"invalid chars", "ref@test", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateGitRef(tt.ref)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateGitRef(%q) error = %v, wantErr %v", tt.ref, err, tt.wantErr)
			}
		})
	}
}

func TestValidatePHPVersion(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{"valid 7.4", "7.4", false},
		{"valid 8.0", "8.0", false},
		{"valid 8.2.1", "8.2.1", false},
		{"empty", "", true},
		{"invalid prefix", "9.0", false}, // 9.x is valid
		{"no minor", "8", true},
		{"too many parts", "8.2.1.1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidatePHPVersion(tt.version)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePHPVersion(%q) error = %v, wantErr %v", tt.version, err, tt.wantErr)
			}
		})
	}
}

func TestValidateNodeVersion(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{"valid", "14.0.0", false},
		{"with v prefix", "v16.13.2", false},
		{"valid", "18.0.0", false},
		{"empty", "", true},
		{"no patch", "16.13", true},
		{"letters", "16.x.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateNodeVersion(tt.version)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateNodeVersion(%q) error = %v, wantErr %v", tt.version, err, tt.wantErr)
			}
		})
	}
}

func TestValidateWebServer(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		ws      string
		wantErr bool
	}{
		{"caddy", "caddy", false},
		{"apache", "apache", false},
		{"nginx", "nginx", false},
		{"ols", "ols", false},
		{"unknown", "lighttpd", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateWebServer(tt.ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateWebServer(%q) error = %v, wantErr %v", tt.ws, err, tt.wantErr)
			}
		})
	}
}

func TestValidateEncoding(t *testing.T) {
	v := NewValidator("/var/www")

	tests := []struct {
		name    string
		enc     string
		wantErr bool
	}{
		{"utf8mb4", "utf8mb4", false},
		{"utf8", "utf8", false},
		{"UTF8", "UTF8", false},
		{"latin1", "latin1", false},
		{"ascii", "ascii", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateEncoding(tt.enc)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEncoding(%q) error = %v, wantErr %v", tt.enc, err, tt.wantErr)
			}
		})
	}
}
