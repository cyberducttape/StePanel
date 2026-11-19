# Security scan — 2026-11-19 (UTC)

**Source:** `e5ddeee2` (`main`), full SHA recorded with each hosted run below
**Toolchain:** Go 1.26.11
**Scope:** current `main`, not a release candidate. The release gate still
requires this recheck on the exact release-candidate source.

## Results

| Check | Where | Result |
| --- | --- | --- |
| `govulncheck` v1.1.4 call-graph scan | local, `14d24925` | PASS: no vulnerabilities found |
| `govulncheck -show verbose` module scan | local, `14d24925` | PASS: no vulnerabilities in the 13 scanned modules or the standard library |
| `staticcheck` 2026.1 | local, `e5ddeee2` | PASS after removing one unused function (U1000) introduced the same day |
| `govulncheck` v1.1.4 | hosted CI integration [44466623034](https://github.com/cyberducttape/StePanel/actions/runs/44466623034), `e5ddeee29156924f5dbec4815c2367e85360ffe6` | PASS: no vulnerabilities found |
| `staticcheck` 2026.1 | same run | PASS: no diagnostics |
| Trivy container scan (CRITICAL, HIGH, fixed only) | same run | PASS: 0 in the `stepanel:ci` Debian 12.15 image and 0 in the `/opt/stepanel/stepanel` Go binary |
| CodeQL (Go, Actions) | hosted, analysis of `e5ddeee2` (34 rules) | 7 results, all previously dismissed alerts; 0 open |
| Code-scanning alerts | GitHub | 0 open; recurring results are previously triaged dismissals (for example `go/cookie-secure-not-set` in `auth.go`, won't fix; several `go/path-injection`, mitigated) |
| Dependabot alerts | GitHub | 0 open |
| Secret-scanning alerts | GitHub | 0 open |

`e5ddeee2` differs from `14d24925` only by removing an unused function and
rewording a comment, so the local `govulncheck` results apply to both.

The previous scan ([2026-10-30](./2026-10-30-local-security-scan-b434e3b.md))
listed one module-level advisory, GO-2026-5932 (`golang.org/x/crypto/openpgp`,
not called). The verbose module scan no longer reports it with
`golang.org/x/crypto` v0.59.0.

Scanned modules: `github.com/distribution/reference` v0.6.0,
`github.com/dustin/go-humanize` v1.1.0, `github.com/google/uuid` v1.6.0,
`github.com/opencontainers/go-digest` v1.0.0,
`github.com/remyoudompheng/bigfft` (2023-01-29), `golang.org/x/crypto` v0.59.0,
`golang.org/x/sys` v0.50.0, `golang.org/x/term` v0.48.0, `modernc.org/libc`
v1.80.1, `modernc.org/mathutil` v1.7.1, `modernc.org/memory` v1.12.1,
`modernc.org/sqlite` v1.62.0.

## Limits

These scans do not replace an independent security review, dynamic testing of
an installed host, or the advisory, patch and credential-rotation drill the
gate asks for.
