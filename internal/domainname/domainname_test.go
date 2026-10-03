package domainname

import (
	"strings"
	"testing"
)

func TestValidateAccepts(t *testing.T) {
	for _, name := range []string{
		"example.com",
		"www.example.com",
		"Example.COM",
		"a.b.c.d.example.co.uk",
		"xn--bcher-kva.example",
		"1password.com",
		"123.example.net",
		"a-b.example.org",
		strings.Repeat("a", 63) + ".com",
		strings.Repeat("a.", 125) + "io",
	} {
		if err := Validate(name); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	for _, name := range []string{
		"",
		"localhost",
		"example.com.",
		".example.com",
		"example..com",
		"-example.com",
		"example-.com",
		"exa_mple.com",
		"exa mple.com",
		"example.com/path",
		"example.com:443",
		"user@example.com",
		"example.com\n",
		"example.com;rm -rf /",
		"*.example.com",
		"1.2.3.4",
		"192.168.0.1",
		"bücher.example",
		"ab--cd.example.com",
		"xn--.example.com",
		strings.Repeat("a", 64) + ".com",
		strings.Repeat("a.", 126) + "io",
		"example.-com",
		"example.123",
	} {
		if err := Validate(name); err == nil {
			t.Errorf("Validate(%q) = nil, want rejection", name)
		}
	}
}

func TestNormalizeLowercases(t *testing.T) {
	got, err := Normalize("WWW.Example.COM")
	if err != nil || got != "www.example.com" {
		t.Fatalf("Normalize = %q, %v", got, err)
	}
	if _, err := Normalize("bad_name.com"); err == nil {
		t.Fatal("Normalize accepted an invalid name")
	}
}

func TestUnicodeRejectionExplainsPunycode(t *testing.T) {
	err := Validate("bücher.example")
	if err == nil || !strings.Contains(err.Error(), "xn--") {
		t.Fatalf("unicode error = %v, want punycode guidance", err)
	}
}
