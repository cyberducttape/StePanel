package stepanel

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestInstallDocsProvideEverySecretTheManifestsRequire keeps the documented
// first-install commands in step with the manifests. A key referenced by a
// manifest but missing from its README makes a fresh install fail with
// CreateContainerConfigError.
func TestInstallDocsProvideEverySecretTheManifestsRequire(t *testing.T) {
	secretKey := regexp.MustCompile(`secretKeyRef:\s*\{?\s*(?:\n\s*)?name:[^\n]*\n?\s*key:\s*([a-z0-9-]+)|secretKeyRef: \{name: [^,]+, key: ([a-z0-9-]+)\}`)
	for manifest, readme := range map[string]string{
		"deploy/kubernetes/stepanel.yaml":              "deploy/kubernetes/README.md",
		"deploy/helm/stepanel/templates/stepanel.yaml": "deploy/helm/stepanel/README.md",
	} {
		manifestData, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		readmeData, err := os.ReadFile(readme)
		if err != nil {
			t.Fatal(err)
		}
		matches := secretKey.FindAllStringSubmatch(string(manifestData), -1)
		if len(matches) == 0 {
			t.Fatalf("%s: found no secretKeyRef keys; update the test pattern", manifest)
		}
		for _, match := range matches {
			key := match[1] + match[2]
			if !strings.Contains(string(readmeData), "--from-literal="+key+"=") {
				t.Errorf("%s requires secret key %q but %s does not create it", manifest, key, readme)
			}
		}
	}

	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"STEPANEL_ENVIRONMENT_KEY", "STEPANEL_BACKUP_SIGNING_KEY", "STEPANEL_ACCOUNT_KEY", "STEPANEL_ADMIN_TOTP_SECRET"} {
		if !strings.Contains(string(readme), "-e "+name+"=") {
			t.Errorf("README container example omits production-required %s", name)
		}
	}
	totpGeneration := "ADMIN_TOTP_SECRET=\"$(openssl rand 20 | base32 | tr -d '=\\n')\""
	for _, example := range []struct {
		path  string
		value string
	}{
		{path: "README.md", value: "-e STEPANEL_ADMIN_TOTP_SECRET=\"$ADMIN_TOTP_SECRET\""},
		{path: "docs/INSTALLATION.md", value: "STEPANEL_ADMIN_TOTP_SECRET=\"$ADMIN_TOTP_SECRET\""},
		{path: "deploy/kubernetes/README.md", value: "--from-literal=admin-totp-secret=\"$ADMIN_TOTP_SECRET\""},
		{path: "deploy/helm/stepanel/README.md", value: "--from-literal=admin-totp-secret=\"$ADMIN_TOTP_SECRET\""},
	} {
		data, err := os.ReadFile(example.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), totpGeneration) || !strings.Contains(string(data), example.value) {
			t.Errorf("%s must generate and pass a valid unpadded Base32 administrator TOTP secret", example.path)
		}
		if strings.Contains(string(data), "BASE32_SECRET") {
			t.Errorf("%s must not use the invalid BASE32_SECRET placeholder", example.path)
		}
	}
}
