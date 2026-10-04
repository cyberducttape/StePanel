package stepanel

import "testing"

func TestValidateOffsiteTarget(t *testing.T) {
	for _, target := range []string{"s3:bucket/stepanel", "b2:bucket/backups", "ssh:host:/srv/backups"} {
		if err := validateOffsiteTarget(target); err != nil {
			t.Errorf("valid target %q rejected: %v", target, err)
		}
	}
	for _, target := range []string{"", "--bad", "bucket/path", "s3:bucket bad"} {
		if err := validateOffsiteTarget(target); target != "" && err == nil {
			t.Errorf("invalid target %q accepted", target)
		}
	}
}

func TestValidBackupNameRejectsRemotePathTraversal(t *testing.T) {
	for _, name := range []string{"20260906-120000.000000000-account", "backup_v2-01"} {
		if !validBackupName(name) {
			t.Errorf("valid backup name %q rejected", name)
		}
	}
	for _, name := range []string{"../account", "/account", "account/other", "account name", ".."} {
		if validBackupName(name) {
			t.Errorf("unsafe backup name %q accepted", name)
		}
	}
}
