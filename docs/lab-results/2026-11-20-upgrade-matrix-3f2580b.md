# N-1 upgrade, interruption, and downgrade matrix

**Run:** [GitHub Actions 44663143624](https://github.com/cyberducttape/StePanel/actions/runs/44663143624)
**Test source:** `3f2580b59bdddc04dd06aceaf6bf3d3a70164b72`
**Completed:** 2026-11-20 UTC
**Runner/guest:** Ubuntu 24.04 GitHub runner; four systemd-container jobs and two QEMU Ubuntu 24.04 guests running Apache.

All six jobs passed:

| Previous release | Environment | Interruption | Result |
|---|---|---|---|
| v0.6.0 | systemd container | none | PASS |
| v0.6.0 | systemd container | candidate verification | PASS |
| v0.6.0 | QEMU guest, Apache, helper fixture | none | PASS |
| v0.7.0 | systemd container | none | PASS |
| v0.7.0 | systemd container | candidate verification | PASS |
| v0.7.0 | QEMU guest, Apache, helper fixture | none | PASS |

Each completed workflow upgraded to the candidate, verified readiness and
preserved state, exercised the documented post-upgrade downgrade, and upgraded
again. The checks include the control-plane backup/snapshot, audit verification,
and backup-layout conversion. The interrupt case stops the candidate during
verification and confirms the retry converges.

The two older published releases require a compatibility fixture: the workflow
removes their old sudo-based database helper so the legacy package can be
installed on current Ubuntu. The QEMU guest exercises their actual
`LoadCredential` handoff. This is not a pristine old-release installation.
During the intentionally broken-candidate scenario the install journal reports
that host-level recovery is incomplete; the test then runs the separately
documented operator downgrade procedure and verifies the previous release,
database snapshot, audit log, and backups. Thus the result validates the
documented recovery path, not automatic completion of every installer rollback.

The tested runtime, upgrade scripts, and helpers match the later `8556e9fe`
source; the source difference is documentation plus the manual-dispatch line
in `.github/workflows/ci.yml`. This is code-equivalent evidence, not a run on
the identical full Git tree. Remaining certification includes populated
installations, further interruption boundaries, supported AlmaLinux/Rocky
hosts, and release-candidate reruns.
