# Implementation and validation report

Local implementation branch: `feat/lxcer-modernization` in the existing fork `https://github.com/muchezz/lxcer`. Upstream remains `https://github.com/pavel1337/lxcer`. Base `eba7adc`. Original author/module/history preserved. No push, PR, release publication, production backup/restore, production LXD connection, or replacement of `/usr/local/bin/lxcer` occurred.

## Implemented

- One modular engine replaces the duplicated pipelines: consistent running/stopped selection, missing/empty-selection errors, nonzero failure/partial exits, bounded host/global workers, process locks, signal cancellation, retries and disk preflight.
- Client/server capability adapters select portable full export, legacy snapshot/image, source-side native filesystem, or mirror refresh workflows. Root drivers and expanded attachments are inventoried. Explicit bind paths and supported managed volume exports are included with per-artifact checksums.
- Private durable versioned manifests and tagged repository receipts record job state, configuration, coverage, destination snapshot IDs, verification levels, errors, cleanup and restore tests. Required verification gates cleanup. Interrupted quiescence can be resumed through recovery.
- Restic repositories use secure environment/file credential sources, independent required/optional status, JSON snapshot identity, tree-path verification, optional data checks and scoped dry-run retention. Local/SFTP/rclone/object-storage locations use Restic's native backend support.
- Original unversioned configuration keeps snapshot/image/zstd defaults and instance archive names. Original restore flags, restore repository/credentials, automatic startup/logging and concurrent restore lists are regression-tested against old and cataloged archives. Modern restore commands remain stopped by default.
- Safe modern stopped restore, exact snapshot selection, archive checksum verification, pool/network remapping, managed volume collision handling, legacy adoption, multi-entry restore lists, isolated health tests, catalog recovery and retained failure journals.
- Structured logs, atomic Prometheus textfile metrics, alert rules/Grafana examples, one-shot systemd scheduling, CI/security checks and manual-only release artifact preparation.

## Tests actually executed

Environment: Linux AMD64, Go **1.26.11**, Restic **0.16.4**, zstd **1.5.5**. Only temporary local Restic repositories and simulated LXD commands were used.

| Check | Result |
|---|---|
| Original baseline | Built; no original tests |
| Added baseline regression before changes | Failed as expected: stopped instance excluded |
| Final `go test -race -json -coverprofile=... ./...` | **69 cases passed (37 top-level tests), no skips/failures; 76.0% statements** |
| Real Restic workflows | Full + actual zstd/decompression, legacy image, and native filesystem backup/verify/restore; `check --read-data` passed |
| Fault and policy tests | Snapshot/export/compression/upload/tree/receipt failures, partial destinations, stopped/missing instances, hooks/resume, lock/concurrency, disk preflight, exclusion, restore/collision/config/health/checksum errors, recovery, legacy adoption, bind paths, managed volumes, refresh mirrors, SSH quoting, secrets and retention scope |
| `gofmt`, `go vet`, staticcheck v0.8.2 | Passed |
| Final password-file override regression | Passed with race-enabled short suite; replaces an existing password environment source |
| govulncheck v1.9.0 | No vulnerabilities found |
| Linux AMD64/ARM64 build | Both built; AMD64 CLI version/config validation executed; ARM64 execution unverified |
| Disposable LXD integration harness | Compiled and safely skipped without explicitly supplied test infrastructure |
| GitHub-hosted workflows | Prepared; not remotely executed because nothing was pushed |

## Compatibility evidence and limits

No real LXD release or Btrfs/ZFS/LVM/directory/Ceph backend was qualified. Simulations cover 3.0.3, 4.0, 5.0, 5.21, 6.0, future version strings and all five driver identities, including old-client/modern-server capability mismatches. These establish adapter decisions, not real server compatibility. The guarded disposable matrix remains required before claiming production support.

Native mode restores files into a new directory; automatic LXD reconstruction from raw filesystem trees is deliberately unavailable. Refresh incrementals delegate transfer optimization to LXD and still create a full mirror export; no storage-native delta archive chain or direct multi-repository streaming is implemented. LXD/SSH transfer bandwidth needs external/server-side controls. Failed resources are retained for review; shared content-addressed images and persistent mirrors are not automatically deleted. Modern automatic restores reject bind/excluded storage dependencies until those are recovered/remapped through a reviewed administrative workflow. Hook scripts and application recovery checks must match the actual application installation. There is no production application verification from these tests.

A root-only profile plus a separate remote flag cannot prove physical isolation: the operator must supply an actual isolated host. Locks coordinate a shared source state directory, not independent controllers. Subprocess error output is suppressed to protect secrets; raw stderr is not retained. Original license terms still need clarification before release redistribution.

## Prepared commits and upstream structure

- `4611924`: baseline audit and stopped-container regression.
- `cc0864a`: working backup/restore engine and automated tests.
- `c2cafb1`: CI, release scripts, monitoring and scheduling examples.
- `d0975b2`: isolation/image ownership hardening and compressed recovery testing.
- `ed8f3b4`: architecture, compatibility, migration, operations, GitLab/Storage Box example and upstream review descriptions.
- `e01bee5`: preserve original config/CLI/archive behavior; expanded profile settings, original backup timestamps, readable metrics and source-only agent credentials.
- Final validation/credential override commit: see `git log`.

Suggested upstream review sequence: baseline regression; engine/compatibility/recovery (optionally split recovery at review time); operations/CI/release preparation. The full PR descriptions and backward-incompatible behavior are documented in DEVELOPMENT.md. All commits remain local until the fork owner approves pushing/submission.

## Changed files

Original versions of replaced legacy files remain in Git history.

```text
A	.github/workflows/ci.yml
A	.github/workflows/lxd-integration.yml
A	.github/workflows/release.yml
M	.gitignore
M	README.md
A	baseline_test.go
A	catalog.go
M	conf.yml
M	config.go
D	container.go
A	core_test.go
A	docs/APPLICATIONS.md
A	docs/ARCHITECTURE.md
A	docs/BASELINE.md
A	docs/COMPATIBILITY.md
A	docs/CONFIGURATION.md
A	docs/DEVELOPMENT.md
A	docs/OPERATIONS.md
A	docs/RESTORE.md
A	docs/VALIDATION.md
A	engine.go
A	engine_test.go
A	examples/lxcer.service
A	examples/lxcer.timer
A	examples/modern.yml
M	go.mod
M	go.sum
D	host.go
D	image.go
A	legacy.go
A	legacy_test.go
A	lxd.go
A	lxd_integration_test.go
M	main.go
A	manifest.go
A	metrics.go
A	monitoring/alerts.yml
A	monitoring/grafana.json
A	restic.go
D	restic_repo.go
A	restore.go
A	restore_test.go
A	runner.go
A	scripts/build-release.sh
D	util.go
```
