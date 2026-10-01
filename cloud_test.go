package main

import (
	"strings"
	"testing"
)

func TestCloudCommandEnvKeepsRcloneConfigButFiltersPanelSecrets(t *testing.T) {
	t.Setenv("RCLONE_CONFIG", "/run/secrets/rclone.conf")
	t.Setenv("STEPANEL_RCLONE_CONFIG", "/wrong/path")
	t.Setenv("STEPANEL_SESSION_SECRET", "must-not-leak")

	env := cloudCommandEnv()
	var hasRcloneConfig, hasPanelSecrets bool
	for _, item := range env {
		if item == "RCLONE_CONFIG=/run/secrets/rclone.conf" {
			hasRcloneConfig = true
		}
		if strings.HasPrefix(item, "STEPANEL_") || strings.Contains(item, "must-not-leak") {
			hasPanelSecrets = true
		}
	}
	if !hasRcloneConfig {
		t.Fatal("cloud command environment omitted RCLONE_CONFIG")
	}
	if hasPanelSecrets {
		t.Fatal("cloud command environment leaked a STEPANEL variable or panel secret")
	}
}

func TestAWSActionArgsUseTypedResourceFlags(t *testing.T) {
	instanceCases := []struct {
		action string
		want   []string
	}{
		{"start", []string{"ec2", "start-instances", "--instance-ids", "i-0123456789abcdef0", "--output", "json"}},
		{"stop", []string{"ec2", "stop-instances", "--instance-ids", "i-0123456789abcdef0", "--output", "json"}},
		{"reboot", []string{"ec2", "reboot-instances", "--instance-ids", "i-0123456789abcdef0", "--output", "json"}},
	}
	for _, test := range instanceCases {
		got, err := awsInstanceActionArgs(test.action, AWSInstanceAction{InstanceID: "i-0123456789abcdef0"})
		if err != nil {
			t.Fatalf("%s args: %v", test.action, err)
		}
		if strings.Join(got, "\x00") != strings.Join(test.want, "\x00") {
			t.Errorf("%s args = %v, want %v", test.action, got, test.want)
		}
	}
	got, err := awsVolumeSnapshotArgs(AWSVolumeSnapshot{VolumeID: "vol-0123456789abcdef0"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ec2", "create-snapshot", "--volume-id", "vol-0123456789abcdef0", "--output", "json"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("snapshot args = %v, want %v", got, want)
	}
}

func TestCloudDNSRecordValidation(t *testing.T) {
	valid := []cloudDNSRequest{{Type: "A", Name: "@", Target: "192.0.2.10", TTL: 300}, {Type: "AAAA", Name: "www", Target: "2001:db8::1", TTL: 300}, {Type: "MX", Name: "@", Target: "10 mail.example.com", TTL: 300}, {Type: "SRV", Name: "_https._tcp", Target: "10 5 443 service.example.com", TTL: 300}}
	for _, record := range valid {
		if !cloudDNSRecordValid(record) {
			t.Errorf("valid record rejected: %#v", record)
		}
	}
	invalid := []cloudDNSRequest{{Type: "A", Name: "@", Target: "example.com", TTL: 300}, {Type: "A", Name: "@", Target: "192.0.2.1", TTL: 10}, {Type: "MX", Name: "@", Target: "mail.example.com", TTL: 300}, {Type: "MX", Name: "@", Target: "70000 mail.example.com", TTL: 300}, {Type: "SRV", Name: "_https._tcp", Target: "10 5 70000 service.example.com", TTL: 300}, {Type: "TXT", Name: "@", Target: "safe\nvalue", TTL: 300}}
	for _, record := range invalid {
		if cloudDNSRecordValid(record) {
			t.Errorf("invalid record accepted: %#v", record)
		}
	}
}

func TestDNSRecordExists(t *testing.T) {
	value := map[string]any{"data": []any{map[string]any{"type": "A", "name": "www.example.com.", "target": "192.0.2.10"}}}
	if !dnsRecordExists(value, cloudDNSRequest{Type: "a", Name: "www.example.com", Target: "192.0.2.10"}) {
		t.Fatal("identical DNS record was not detected")
	}
}
