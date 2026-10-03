// Package domainname is StePanel's single validator for host names that reach
// web server configuration, certificates, or privileged helpers. HTTP
// handlers, the typed root broker, and the broker's helper schema all call
// Validate; the shell helpers keep equivalent (lowercase-only) regexes as
// defence in depth.
//
// Policy:
//   - ASCII only. Internationalized names must be submitted in their A-label
//     (punycode, "xn--") form; Unicode input is rejected rather than converted,
//     so every layer sees the same bytes and no normalization differences or
//     homograph conversions happen implicitly.
//   - "xn--" labels must satisfy the LDH rules below. StePanel does not decode
//     them; DNS and the certificate authority remain the authority on whether
//     an A-label is registrable.
//   - Other labels with hyphens in the third and fourth positions ("ab--")
//     are reserved (RFC 5891) and rejected.
//   - At least two labels, each 1-63 letters, digits, or hyphens, not starting
//     or ending with a hyphen; at most 253 characters; no trailing dot.
//   - The top-level label must not be all digits, so IPv4 literals are not
//     names.
//   - Matching is case-insensitive; Normalize returns the lowercase form.
package domainname

import (
	"errors"
	"fmt"
	"strings"
)

const (
	maxNameLength  = 253
	maxLabelLength = 63
)

// Validate reports whether name is an acceptable host name under the policy.
func Validate(name string) error {
	if name == "" {
		return errors.New("domain is required")
	}
	if len(name) > maxNameLength {
		return fmt.Errorf("domain exceeds %d characters", maxNameLength)
	}
	for i := 0; i < len(name); i++ {
		if name[i] >= 0x80 {
			return errors.New("domain must be ASCII; submit internationalized names in punycode (xn--) form")
		}
	}
	if strings.HasSuffix(name, ".") {
		return errors.New("domain must not end with a dot")
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return errors.New("domain must contain at least two labels")
	}
	for _, label := range labels {
		if err := validateLabel(label); err != nil {
			return fmt.Errorf("domain label %q: %w", label, err)
		}
	}
	tld := labels[len(labels)-1]
	if strings.Trim(tld, "0123456789") == "" {
		return errors.New("top-level label must not be numeric")
	}
	return nil
}

// Valid is Validate as a predicate.
func Valid(name string) bool {
	return Validate(name) == nil
}

// Normalize validates name and returns its canonical lowercase form.
func Normalize(name string) (string, error) {
	if err := Validate(name); err != nil {
		return "", err
	}
	return strings.ToLower(name), nil
}

func validateLabel(label string) error {
	if label == "" {
		return errors.New("label is empty")
	}
	if len(label) > maxLabelLength {
		return fmt.Errorf("label exceeds %d characters", maxLabelLength)
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return fmt.Errorf("invalid character %q", c)
		}
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return errors.New("label must not start or end with a hyphen")
	}
	if len(label) >= 4 && label[2] == '-' && label[3] == '-' {
		if !strings.EqualFold(label[:2], "xn") {
			return errors.New("labels with hyphens in positions 3 and 4 are reserved")
		}
		if len(label) == 4 {
			return errors.New("punycode label is empty")
		}
	}
	return nil
}
