# Quota-smoke fixture failure — 2026-10-19

**Observed run:** [quota-enabled production install smoke #39183265054](https://github.com/cyberducttape/StePanel/actions/runs/39183265054)  
**Failing setup command:** `mkfs.ext4 -F -O project,quota -E quotatype=usrquota,prjquota /dev/vdb`

The disposable Ubuntu guest stopped during cloud-init filesystem setup, before
StePanel installation or project-quota enforcement was tested. e2fsprogs
rejected the comma-separated `quotatype` list (`Bad option(s) specified:
prjquota`). This was a test-fixture defect, not an application installation
failure; the run provides no quota-enforcement evidence.

The fixture now uses e2fsprogs' documented colon-delimited list:
`quotatype=usrquota:prjquota`. The same command is used by the GitHub Actions
smoke and local KVM certification harness. Validation on local e2fsprogs
1.47.2 accepted the corrected command in no-write mode; shell syntax and the
quota-fixture capacity check also pass. A successful hosted rerun is still
required to establish that the supported guest's formatter, mount options,
quota activation, and StePanel workflow all work together.

Until the exact-source quota-enabled smoke completes successfully, installed
project-quota enforcement remains an open production gate.
