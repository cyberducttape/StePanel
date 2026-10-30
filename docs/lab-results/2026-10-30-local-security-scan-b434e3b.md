# Local Go security scan — 2026-10-30

**Source:** `b434e3b` (`main` worktree)
**Toolchain:** Go 1.26.10

## Results

| Check | Result |
| --- | --- |
| `govulncheck` v1.1.4 call-graph scan | PASS: zero reachable vulnerabilities |
| `govulncheck -show verbose` module scan | REVIEW: one module-level advisory, GO-2026-5932 |
| `staticcheck` 2026.1 | PASS: no diagnostics |

GO-2026-5932 concerns `golang.org/x/crypto/openpgp`, which is unmaintained and
has known security issues; the advisory lists no fixed version. The application
depends on `golang.org/x/crypto` for `ssh` and `bcrypt`, but the scan found no
symbol or package reachability for `openpgp`. The finding is not suppressed.
Re-run the scan after dependency or import changes, and revisit this advisory
if the project adds OpenPGP use or a replacement dependency becomes available.

The scan does not replace CodeQL, container scanning, an independent security
review, or host-level security testing. The exact-source hosted security jobs
remain pending for this unpushed commit.
