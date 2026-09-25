package main

import (
	"fmt"
	"os"
	"strings"
)

// failureInjection implements the deterministic fault hook used by recovery
// tests and disposable-host drills. The hook is inert unless STEPANEL_FAIL_AT
// is set. Its value is operation:point, for example restore:commit or
// deploy:init. A wildcard point (operation:*) fails at every instrumented
// point for that operation.
func failureInjection(operation, point string) error {
	spec := strings.TrimSpace(os.Getenv("STEPANEL_FAIL_AT"))
	if spec == "" {
		return nil
	}
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("invalid STEPANEL_FAIL_AT %q: expected operation:point", spec)
	}
	if !strings.EqualFold(strings.TrimSpace(parts[0]), operation) {
		return nil
	}
	wantedPoint := strings.TrimSpace(parts[1])
	if wantedPoint != "*" && !strings.EqualFold(wantedPoint, point) {
		return nil
	}
	return fmt.Errorf("failure injection at %s:%s", operation, point)
}

func transactionFailureOperation(kind string) string {
	kind = strings.ToLower(kind)
	switch {
	case strings.Contains(kind, "backup"):
		return "backup"
	case strings.Contains(kind, "restore"), strings.Contains(kind, "import"), strings.Contains(kind, "cpmove"), strings.Contains(kind, "wordpress"):
		return "restore"
	case strings.Contains(kind, "deploy"), strings.Contains(kind, "git"), strings.Contains(kind, "staging"):
		return "deploy"
	default:
		return kind
	}
}
