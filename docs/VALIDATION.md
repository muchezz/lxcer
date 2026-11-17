# LXCER critical-gap validation

Branch: `feat/lxcer-modernization`, fork `muchezz/lxcer`. This work continues from `5351441` and preserves all seven earlier modernization commits. No rewrite/restart, production LXD access, production credential access, push, deployment, PR or release publication occurred. `/usr/local/bin/lxcer` was not replaced.

## Production-readiness assessment

**FAIL — not cleared for production deployment.** The implementation and available local checks are complete, but no disposable LXD servers, SFTP test repository or privileged disposable storage environment were supplied. Every real LXD/storage combination remains **NOT VERIFIED**. Qualify the guarded workflows, provision scratch quotas/space and validate application recovery before deploying.

PASS means the named check actually passed at the stated layer. It does not promote mocked commands to real daemon or storage verification. NOT VERIFIED means the required infrastructure/evidence is absent.

## Implemented fixes and executable evidence

| Workstream | Status | Implementation/evidence |
|---|---|---|
| LXD 3.0.3 / 4.0 / 5.0 / 6.x adapters | PASS: command models | API extensions plus client command help select image/full operations. 3.0.3 never executes `lxc export`. Client name-override capability is negotiated. |
| Running/stopped backup and restore | PASS: command models | 40 version × driver × state cases; exact selection, old import-then-rename and isolated stopped targets. |
| Storage detection | PASS: command models | Btrfs/ZFS/LVM/dir/Ceph paths; unsupported native incrementals fail or explicitly fall back. Source capacity is recorded or marked unknown. Strict mode rejects unknown capacity. |
| Automated native reconstruction | PASS: real local Restic/GNU tar, modeled LXD | Native roots restore to private staging, build a unified image, import/init and validate metadata/ownership. Captured ID maps, ACL IDs, capability root IDs and symlinks have focused coverage. |
| Native Btrfs/ZFS incrementals | PASS: command/state models | Full then `btrfs send -p` / `zfs send -i`, immutable snapshots, persisted parents/checkpoints, checksum-verified receive, bounded output. No repeated full LXD export. |
| Recoverable chains and fallback | PASS: models | Missing required parents fail; explicit full restart removes broken dependencies. Optional repositories cannot verify a child without parents. Retention groups whole chains and protects unfinished chains. |
| Archive correctness | PASS | Unified/split exports, checksums, missing/empty exports, failed compression, no snapshot ID, absent tree nodes, partial destinations and receipt failures. |
| Space/preflight | PASS: local/fault tests | 32 GiB configurable monitored staging default, live free-space checks, bounded stream writer, source-pool exhaustion rejection and recursive source/state exclusion. |
| Recovery/locking | PASS: models/local kernel locks | Duplicate locks rejected, interrupted hooks resumed, verified checkpoints survive cleanup-only errors, active jobs excluded from cleanup. |
| Observability/security/coexistence | PASS: local | Structured logs, secret sources, atomic readable metrics, upload/capture/staging/chain gauges, alert rules and explicit quoted SSH agent binary paths. Original config/flags/startup/archive workflows retained. |
| Realistic legacy scenario harness | PASS: compilation/guards; execution NOT VERIFIED | Two disposable hosts, exact 3.0.3/Btrfs source, running payload/heartbeat and stopped instance, one required SFTP destination, checksum restore, injected missing tree must exit nonzero and retain artifacts. |
| Actual application/database consistency | NOT VERIFIED | Hooks execute and failures/resume are tested; real PostgreSQL/MySQL/MongoDB/GitLab recovery needs deployment-specific qualification. |

## Tests and quality results

Environment: Linux AMD64, Go 1.26.11, Restic 0.16.4, zstd 1.5.5 and GNU tar. Default tests used temporary local repositories and simulated LXD/storage commands. No default LXD daemon was contacted.

| Check | Status | Recorded result |
|---|---|---|
| One complete race/coverage run | FAIL initially: obsolete test assertion | 127 cases: 126 passed, one dry-run assertion failed because it classified `lxc import --help` as a mutation. No data race warnings. Measured coverage **71.9%**. |
| Corrected assertion and final short/race suite | PASS | **123 cases passed, 3 deliberately slow integration tests skipped**, 55 top-level tests including the new cases; measured short-suite coverage **66.9%**. The capability probe is read-only; the assertion now distinguishes it from import. |
| Full local Restic export/zstd/restore | PASS | Full archive round trip, repository data check and decompression; 11.94 s in the complete run. LXD operations modeled. |
| Local legacy image/Restic round trip | PASS | Image archive backup/verification/restore; 11.86 s. LXD operations modeled. |
| Local native filesystem/Restic round trip | PASS | Raw filesystem restore with Restic data verification; 9.81 s. |
| Automated native image reconstruction | PASS | Real Restic restore plus real GNU tar, modeled init/config/health/cleanup; 8.83 s. |
| Original flag/config/archive compatibility | PASS | Real Restic/zstd, old archives and new catalog, automatic legacy startup, concurrent restore lists; 17.98 s. LXD operations modeled. |
| `gofmt`, `go vet`, staticcheck v0.8.2 | PASS | Static naming warnings corrected; final source checks clean. |
| govulncheck v1.9.0 | PASS | No vulnerabilities found. Dependencies unchanged after scanning. |
| actionlint v1.7.7 | PASS | Disposable custom runner label declared; all workflow definitions validated. |
| Real LXD/SFTP/kernel harnesses | NOT VERIFIED | All three compiled and skipped because disposable opt-in/infrastructure was absent. Explicit opt-in with invalid prerequisites fails instead of reporting a green skip. |
| Linux AMD64/ARM64 artifacts | PASS builds/checksums | Built once from implementation commit `94620f1`; AMD64 version/config commands passed. ARM64 execution remains NOT VERIFIED. |
| GitHub-hosted workflow execution | NOT VERIFIED | Workflows prepared locally; no push or remote workflow execution. |

The slow real Restic cases all passed during the full run and were not repeated after the test-only assertion fix. Final short/focused checks cover the subsequent mountpoint, coexistence and recursive-state safeguards. Across those runs, all 129 current cases have passing evidence at their stated layer. Coverage values are separate measurements, not a fabricated merged/final-full result. No extra long benchmark or repeated full suite was run.

## Verified LXD versions and storage backends

| Target | Adapter tests | Real daemon/backend workflow |
|---|---|---|
| LXD 3.0.x, especially 3.0.3 | PASS for 3.0.3 model, generic capability fixtures | NOT VERIFIED |
| LXD 4.0 LTS | PASS model including import-then-rename | NOT VERIFIED |
| LXD 5.0 LTS / modern 5.x | PASS models/capability fixtures | NOT VERIFIED |
| LXD 6.x | PASS models/capability fixtures | NOT VERIFIED |
| Btrfs | PASS portable + stream command models | NOT VERIFIED |
| ZFS | PASS portable + send/receive + legacy-mountpoint models | NOT VERIFIED |
| LVM | PASS portable/explicit fallback models | NOT VERIFIED |
| Directory | PASS portable/explicit fallback models | NOT VERIFIED |
| Ceph | PASS portable/explicit fallback models | NOT VERIFIED |
| Restic local repository | PASS real temporary repositories | PASS local data checks/recovery |
| Restic SFTP / rclone / object stores | Native Restic backend pass-through; SFTP harness implemented | NOT VERIFIED |

Btrfs tooling was available but no explicitly disposable filesystem was configured. ZFS tooling/environment was absent. The installed LXC client was not used to inspect the default/production daemon.

## Incremental capabilities and measurements

- `storage-incremental` produces genuine source-side Btrfs/ZFS parent-relative streams. `incremental` retains the original mirror-refresh/full-export semantics for compatibility; Restic deduplication is not labeled a source incremental.
- Manifests record stream kind, dependencies, SHA256, size, capture duration, staging peak, packed upload bytes and available referenced/exclusive snapshot bytes. Missing driver measurements use `-1`.
- The protocol model deliberately emits a 32 KiB full stream and 1 KiB delta. **These are simulated sizes, not measured Btrfs/ZFS performance.** Real transfer reduction, storage consumption and timing remain NOT VERIFIED until the kernel harness runs.
- The real kernel harness writes 8 MiB, changes 4 KiB in place, replays the chain through Restic, validates the recovered image payload and reports sizes/durations. Its LXD import remains modeled; real LXD qualification is separate.
- Full-chain rotation defaults to eight members. Retention cannot prune retained parents. Missing parents fail unless a full restart was explicitly configured. Source snapshots advance only after successful required verification/receipts.

## Remaining operational risks

1. **Real interoperability is NOT VERIFIED.** Run the disposable version/driver matrix, real kernel stream harness and two-host legacy SFTP scenario before production. They cannot establish actual application recovery without relevant health procedures.
2. Native reconstruction needs correct architecture/root/ID-map metadata, GNU tar and sufficient ownership/xattr/storage privileges. Unsupported/missing metadata and unmapped owners fail safely. Virtual-machine filesystem reconstruction and nested datasets/subvolumes require other qualified strategies/policies.
3. Staging monitoring can overshoot between samples for external exporters. Use filesystem/project quotas for a strict physical limit, size source LXD pools for publication/application growth, and raise the default staging cap deliberately for large backups. Unknown source capacity is not a verified preflight; strict mode can reject it.
4. Legacy image publication still retains content-addressed image data to avoid deleting shared/unrelated resources. Budget and review those source images; there is no global image deletion. Failed source snapshots, receivers and archives remain until reviewed. Chain-aware retention protects incomplete chains and may retain extra repository data.
5. Modern restores keep applications stopped and restore tests use root-only devices, but a remote name/flag cannot prove physical isolation. Supply genuinely separate test infrastructure. External excluded/bind data still needs explicit independent recovery/remapping; managed-volume restoration is automated when supported.
6. Locks coordinate shared source state, not independent controllers using unrelated state directories. Stable agent identities must be configured before relying on multi-host retention scope. Independent LXD/SSH traffic limits and transport overhead remain outside Restic's upload-byte reporting.
7. Repository locks are never forcibly removed; repository-wide prune is not automatic. Protect catalog receipts and recover metadata through Restic after local state loss. Unfinished resources remain journaled.
8. Original redistribution license terms still need clarification before publication. No release publication is authorized here.

## Prepared changes

The original seven commits are unchanged. New local commits:

- `ddac266`: native reconstruction, storage streams/chains, compatibility, capacity/staging safety, metrics, coexistence and regressions.
- `94620f1`: guarded real LXD 3.0.3/SFTP and kernel-storage harnesses, expected version/backend checks and CI workflow validation.
- Documentation/configuration report commit: see `git log`.

See [STORAGE_STREAMS.md](STORAGE_STREAMS.md), [COMPATIBILITY.md](COMPATIBILITY.md), [RESTORE.md](RESTORE.md) and the examples for executable procedures. Local binary artifacts coexist with `/usr/local/bin/lxcer`; nothing is installed automatically.
