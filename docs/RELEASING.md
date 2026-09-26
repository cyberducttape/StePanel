# Releasing StePanel

Release tags must match the version recorded in `version.go`. This document
describes the release procedure, applicable to any version.

## Release Checklist

1. Update `version.go` with the new version number. Keep the following
   synchronized with `version.go`:
   - `CHANGELOG.md` (add entry for this version)
   - Any migration notes or upgrade guides
   - Helm, Kubernetes, Terraform chart versions
   - OpenAPI document version
2. Run `make audit` to execute formatting, vet, unit tests, race tests,
   repository recovery drills, and release metadata validation. `make
   recovery-drill` can be run separately and writes evidence to
   `/tmp/stepanel-recovery-drills.md`. `make release-check` separately verifies that
   the Go version, Helm chart, OpenAPI document, and changelog agree. The Make
   targets use bounded Go concurrency by default so the gate is safe on modest
   hosts. For a severely constrained machine, run `make audit TEST_PROCS=1`.
   The recovery runner sets `GOFLAGS=-p=1` by default and executes each drill
   group serially. A host that is near its process/task limit can still fail
   subprocess-heavy drills; treat that as an infrastructure gate failure and
   rerun on a disposable host with capacity before accepting the release.
   The release must also pass the N-1 upgrade/rollback lab and the destructive
   host-recovery acceptance described in [`STATE.md`](STATE.md) and
   [`DESTRUCTIVE_RECOVERY_LAB.md`](DESTRUCTIVE_RECOVERY_LAB.md); repository
   unit drills alone are insufficient. Record achieved recovery times against
   [`DR_OBJECTIVES.md`](DR_OBJECTIVES.md).
3. Review the generated release notes and confirm the supported upgrade path.
   Release automation rejects tags that do not match `version.go`.
4. Create and push an annotated tag:

   ```sh
   release_version=$(sed -n 's/const Version = "\([^"]*\)"/\1/p' version.go)
   git tag -a "v${release_version}" -m "StePanel v${release_version}"
   git push origin "v${release_version}"
   ```

5. The release workflow runs GoReleaser for Linux AMD64 and ARM64 installer
   archives containing the binary and installer support tree,
   creates tar archives and SHA-256 checksums, publishes provenance, pushes
   multi-architecture container images, and attaches the artifacts to the
   GitHub release. The workflow also attaches an SPDX SBOM generated for the
   checked-out source tree.

Never include database passwords, backup archives, production configuration, or
session secrets in release artifacts.

## Architecture checkpoint acceptance

Before promoting the stabilization branch, verify the durable control plane,
worker, and recovery behavior together:

```sh
GOMAXPROCS=1 GOCACHE=/tmp/stepanel-go-cache GOFLAGS=-p=1 make audit
```

Confirm that `/readyz` fails for a corrupt control-plane database, unresolved
dead-letter jobs, and pending required resource enforcement. On a disposable
host, also exercise `stepanel-worker.service`, control-plane backup/restore,
site termination recovery, and the domain-claim gate. The feature catalog and
production-gap analysis remain authoritative for capabilities that are still
operator-only or require external provider adapters.
