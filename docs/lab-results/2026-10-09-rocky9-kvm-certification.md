# Rocky Linux 9 KVM certification run — 2026-10-09

This is the first run of StePanel's install smoke, the **full** recovery
matrix, and abrupt-loss drills on a hardware-virtualized RHEL-family guest
with SELinux enforcing. CI runs the same smokes in containers (no SELinux)
and only the bounded recovery matrix. This run is release evidence for the
scenarios listed below only; it is not a certification of physical power
loss, multi-host recovery, or the recovery-time SLA.

## Environment

| Item | Value |
| --- | --- |
| Harness | `deploy/lab/local-kvm-certification.sh` |
| Guest | Rocky Linux 9 GenericCloud, SHA-256 `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8` |
| Guest kernel | `5.14.0-687.10.1.el9_8.0.1.x86_64`, SELinux **enforcing** |
| Guest resources | 4 vCPU, 3 GiB RAM, 20 GiB root, 3 GiB ext4 `/var/www` with user quotas, 96 MiB ext4 for the ENOSPC drill |
| Host | QEMU 10.2.1 with KVM, Linux 7.0.0 |
| Stack | Caddy, MariaDB, PHP-FPM, production install (`STEPANEL_QUOTA_SMOKE=1`) |
| Build | `e62ab63` plus the uncommitted fixes that became `62033e0` and the two new lab scripts |

## Results

| Phase | Result | Notes |
| --- | --- | --- |
| Boot and quota setup | PASS | 81 s to cloud-init complete |
| Install, workflow smokes, bounded matrix | PASS | 57 drills, including cpmove import, SFTP/FTPS, scheduled tasks, Python, app lifecycle, deploy recovery |
| Full recovery matrix | PASS | 105 drills over two segments (see below) |
| Abrupt guest loss, idle | PASS | QEMU `SIGKILL`; services and `/readyz` back in 23 s including guest boot |
| Abrupt guest loss during a backup | PASS | 300 MiB site; guest killed mid-archive; after reboot the job completed, the listed backup verified, no staging remained, a fresh backup completed and verified (262 s) |
| Real ENOSPC on a dedicated filesystem | PASS | backup failed with ENOSPC; nothing partial was published |
| Rootless build runner | **FAIL**, then fixed | SELinux, see below; passes after the fix |

### Full recovery matrix

Every supported journal boundary was killed and recovered on the real host:
`cpmove:activate`; `backup:init|archive|verify|commit`;
`restore:database|verify|extract|activate|commit`;
`terminate:init|backup|database|routes|proxies|tasks|services|site-state|ownership`;
`suspend:before-persist|persisted`. Each case also exercised a file restore,
a database restore and a termination at known-good boundaries.

The harness's 90-minute install budget expired after the
`restore:extract` case; the guest was healthy (`/readyz` 200). The remaining
cases (`restore:activate`, `restore:commit`, all termination and suspension
boundaries) ran on the same guest and build. The harness now allows four
hours and runs long phases detached from the SSH session.

`restore:provision` and `restore:provisioned` are reached only by the
restore-to-staging database path, which no real-host drill exercises yet.

## Defects found and fixed by this run

All were invisible to container-based CI.

1. **Scheduled tasks never ran on SELinux hosts.** systemd may not execute
   the task script (`var_lib_t`): `avc: denied { execute } ... tcontext=...:var_lib_t`.
   Tasks now run via `/bin/bash`; Python apps start `gunicorn` via the
   virtualenv's `python -m`. (`e62ab63`)
2. **Git deploys failed after any seal.** Sealing applied the panel ACL before
   `chmod 2750`, which reset the ACL mask to `r-x` and removed the panel's
   write access ("unable to create release staging directory"). This also
   failed the GitHub installation smokes. (`e62ab63`)
3. **The full matrix could not pass.** Restore staging checkpoints were sent
   to paths that never reach them, and generated account names exceeded the
   32-character limit. (`62033e0` and later)
4. **The suspension drill asserted the wrong contract.** A panel killed
   before a suspension is persisted never acknowledged it; the drill now
   requires the account to be unchanged in that case and suspended only
   after `suspend:persisted`.
5. **The ENOSPC test counted ext4's `lost+found`** as a published artifact.

## Rootless build runner under SELinux (fixed the same day)

The run first failed to pull images:

```
avc: denied { nnp_transition } comm="(podman)" tcontext=...:container_runtime_t
avc: denied { create } comm="podman" name="db.sql" tcontext=...:httpd_sys_content_t
```

Investigation on the same guest found five independent faults:

1. Unit options that make systemd set `no_new_privs` (`ProtectKernelTunables`,
   `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectClock`,
   `RestrictRealtime`, `RestrictAddressFamilies` on systemd 252) blocked both
   `newuidmap`'s setuid transition and podman's transition into
   `container_runtime_t`. They were removed; `ProtectProc`, `ProtectSystem`,
   `ProtectHome`, `PrivateTmp`, the closed device policy, and resource limits
   remain, and containers now run confined as `container_t`.
2. Podman storage lived in the site tree (web content). It now lives in
   `/var/lib/containers/stepanel-runner/<site user>` (container storage label).
3. Podman 5 records its run directories on first use; the runner used a new
   random runtime directory per build, so every build after the first failed.
   The runtime directory is now stable per site.
4. `systemd-run --pipe` fails inside a system service ("Connection reset by
   peer"), so builds started through the root broker -- every release
   pipeline -- never ran. The unit now writes to a root-owned log.
5. The release staging tree is private to the panel account, so the site
   account could not read it. Root now copies it into a root-owned directory
   without following links and hands the copy to the site account.

The live document root was also mounted with `:Z`, which relabels it with a
private container label; builds now use the copy, and the artifact is
restored to the site's label before publication. A failed build no longer
empties the previous artifact.

Acceptance on the same SELinux-enforcing guest:
`runner-perm-smoke.sh` (two builds, a failed build that must keep the last
good artifact, source visibility, label checks) and
`release-pipeline-smoke.sh` (`octocat/Hello-World` checked out, built with a
pinned image, validated, atomically activated, runner state cleaned up) both
pass with **no SELinux denials**; `restorecon -nRv` on the activated document
root reports nothing to relabel. HTTPS serving of the test domain was not
checked because Caddy cannot obtain a certificate for `.example.test`.

## Reproducing

```sh
deploy/lab/local-kvm-certification.sh Rocky-9-GenericCloud.latest.x86_64.qcow2
```

Verify the image against Rocky's `CHECKSUM` first. The run directory must be
on disk, not tmpfs; a full run takes about four hours.
