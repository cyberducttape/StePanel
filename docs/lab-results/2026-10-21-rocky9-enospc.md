# Rocky Linux 9 real-ENOSPC recovery check

Status: focused lab check passed; this is not full-host recovery certification.

## Reproduction

- StePanel source and test binary: commit `687613a3c6fa50141b9339aaa7c52a76fe94a73c`
- Guest: Rocky Linux 9 Generic Cloud x86_64, image SHA-256
  `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8`
- Guest kernel: `5.14.0-687.10.1.el9_8.0.1.x86_64`
- Filesystem: ext4 on a dedicated guest block device; user and project quotas
  both reported enabled on `/var/www`; mount options included `quota,usrquota,prjquota`.
- Harness: `deploy/lab/local-kvm-certification.sh`, `boot` then `enospc`, QEMU
  with 2 vCPUs and 2 GiB RAM.
- Tests: `TestBackupRecoversFromRealENOSPC` and
  `TestRestoreFilesRecoversFromRealENOSPC`.

## Result

Both tests passed on the rerun under actual quota-enforced ENOSPC. The backup
test completed in 1.86 seconds and the file-restore test in 2.02 seconds; the
guest test phase took 31 seconds including setup. The restore assertion now
allows the persistent `.stepanel-stage-coordination.lock` inode and continues
to reject unexpected staging artifacts.

The first attempt ran the same guest phase before that assertion was corrected:
backup recovery passed, while restore recovery failed only because the test
expected the import root to contain the filler file alone. The persistent lock
was expected by the staging-lock design but absent from that assertion. The
assertion was fixed, the full Go suite passed locally, and the rebuilt binary
from the commit above passed both tests. Both attempts are retained in the
local harness `results.tsv`; the passing test output is in `enospc.log`.

## Scope and limitations

This demonstrates recovery from ENOSPC for these two focused backup/file-restore
paths on one Rocky 9 ext4 guest. It does not certify MariaDB restore, complete
installed-host recovery, power-loss behavior, repeated recovery matrices,
quota behavior on XFS, or production readiness. Those remain separate release
gates in `V1_PRODUCTION_GATES.md`.
