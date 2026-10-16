# Current-head validation — 2026-10-16

## Source and environment

| Item | Result |
| --- | --- |
| Source commit | `64696a2abcdbdab90c645a56db103bba7729b21f` |
| Worktree | Clean before this documentation-only evidence update |
| Go toolchain | `go1.26.7 linux/amd64` |
| Hosted CI | [Run 38833209036](https://github.com/cyberducttape/StePanel/actions/runs/38833209036) — passed |
| CodeQL | [Run 38833209088](https://github.com/cyberducttape/StePanel/actions/runs/38833209088) — passed |
| Open CodeQL alerts | 0 at capture from the GitHub code-scanning API |

## Local checks

| Check | Result |
| --- | --- |
| `go test ./...` | Passed; root package 65.436 s; all packages passed |
| `go test -race ./...` | Passed; root package 441.531 s; all packages passed |
| `go vet ./...` | Passed |
| `bash -n install.sh` and all `deploy/` and `scripts/` shell files | Passed |
| `bash scripts/check-docs-links.sh docs` | Passed |
| `python3 scripts/check-docs-references.py` | Passed; 88 documents checked |

Hosted CI additionally passed its configured coverage thresholds, race suite,
recovery drills, mixed-load checks, container and deployment validation, and UI
E2E job. This report does not claim that a full `scripts/production-gate.sh` or
an installed-host destructive recovery drill was run on this exact docs commit.

## Installed-host evidence still pending

The exact-source 20-iteration cohort uses ten two-iteration dispatches on
`60f96eee4063afda2ce0292aabde2c0d6cea061e`, whose application code is unchanged
from `64696a2`. At capture, shards 01 and 02 were in progress across all six
Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 × Apache/Caddy combinations;
shards 03–10 were queued. No iteration from an unfinished shard is counted.
The exact-head install and quota workflows were also pending. Existing completed
six-host WordPress and quota-backed guest evidence is linked from the authoritative
gate document, but neither substitutes for this cohort.

This validation does not close physical host power loss, ENOSPC during
publication/import/database restore, live SMTP/webhook outage and recovery,
capacity certification, OpenLiteSpeed/runtime support, N-1 upgrade policy, or an
independent security review. StePanel remains Operator Beta and is not approved
for production release.
