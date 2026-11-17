# Development, releases and upstream contribution

The module remains `github.com/pavel1337/lxcer` for original project compatibility. Develop only in the existing `muchezz/lxcer` fork with upstream `pavel1337/lxcer`. No fork, push, PR or release is created automatically.

## Quality gates

```sh
gofmt -w *.go
go vet ./...
go test -race -coverprofile=coverage.out ./...
go build -trimpath ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

Go 1.26 is the minimum project version; the module pins Go 1.26.11 for reproducible local builds. `go test -short` skips real Restic integration. Default tests invoke only simulated LXD commands, temporary local Restic repositories and disposable local subprocesses. They never connect to the installed production LXD daemon. Real Restic tests skip if Restic is absent; CI installs it so the check is mandatory there. The separate build-tagged LXD suite requires the explicit disposable-host environment documented in compatibility.md.

The PR workflow checks formatting, vet, tests/races, builds, static analysis and vulnerabilities. The manual LXD workflow runs only on the dedicated `lxcer-disposable` runner/environment. Configure protection for that environment. No automated release publication workflow exists.

## Release preparation

`./scripts/build-release.sh vNEXT` builds Linux AMD64/ARM64 binaries with CGO disabled, trimmed paths, fixed commit/date metadata, SHA256 sums and a Git-history changelog. The workflow_dispatch release action uploads reviewable CI artifacts only. Review the changelog for the release interval, confirm license terms and qualify LXD/storage combinations before approving publication. Reproduce with the same Go patch version, source commit and build flags; Go toolchain changes can change binary hashes.

Install side-by-side at `/opt/lxcer/VERSION/lxcer`, validate configuration and run disposable backup/restore checks. Stop scheduling, preserve state/config, and switch the service binary only after approval. Upgrade Restic and client versions in staging first. Rollback by restoring the previous binary/config together; the original binary cannot interpret v1 manifests or safe restore commands. Do not run its destructive cleanup. Repository data snapshots remain accessible with Restic even if rolling back the orchestrator. Keep catalog receipts and the previous toolchain/build metadata.

## Proposed upstream PRs

1. **Reliability baseline and regression tests.** Audit and reproducible stopped-instance selection failure; documents unsafe cleanup/error loss.
2. **Backup engine and compatibility adapters.** Strict migrated configuration, bounded scheduler, capabilities/storage inspection, state manifests, locks, hooks, strategies, secure runner, verified Restic destinations and ownership cleanup. These files form one working change rather than disconnected interfaces.
3. **Recovery and catalog workflows.** Stopped safe restores, legacy archive adoption, multi-source restore lists, mappings, managed volumes, isolated test ownership and crash resume handling. Rebase/split this from the engine commit if maintainers prefer staged review.
4. **Operations and release engineering.** Textfile metrics, alerts/dashboard, scheduling, docs, disposable compatibility harness and CI/artifact preparation.

Each PR description should state the actual behavior, validation and unverified real-host matrix. Keep these local commits until the fork owner approves pushing. Never submit to upstream automatically.

### Review description: engine

The old local selection skipped stopped containers and ignored `-container`; pipeline errors could still delete the archive and return success. Replace duplicated host pipelines with one locked job engine that selects running/stopped instances, persists operation state, verifies each required Restic snapshot/path and retains staging on any incomplete result. Use server extensions plus client command detection for legacy image and portable export paths. Validate with failure injection, race tests and isolated real Restic repositories; real LXD combinations remain unqualified.

### Review description: recovery

Import only after restoring and checking the exact archive checksum. Refuse name collisions, keep modern restores stopped while preserving original flag-based startup, map explicit storage/network devices and record test-owned resources. Recover local catalog metadata from tagged repository receipts and adopt older archives with explicit instance metadata. Validate collision, checksum, failed import/configuration/health, interrupted resume and filesystem round trips; application recovery still requires an audited health procedure.

### Review description: operations

Add bounded-label Prometheus textfile metrics, alert/dashboard examples, strict secret sources, documented compatibility and external-disk coverage, CI quality checks and manually prepared AMD64/ARM64 artifacts. No production deployment, repository push, release publication or upstream submission occurs.

## Deliberate compatibility changes

Failures and partial results now return nonzero. Stopped containers are included. Empty/missing selection is an error. Explicit selection never silently chooses other instances. External disks require capture or an acknowledged exclusion. Required destination verification and catalog receipts gate cleanup. Modern restore commands stay stopped; original flag-based restores with unversioned configuration still start unless `-stopped` is supplied. Destructive overwrite is unavailable. `-cleanup` is rejected. Plaintext YAML passwords remain supported for migration but examples use protected sources. Original restore configuration/repository selection and old archive restores remain usable without manual adoption. Explicit adoption is available for modern metadata/mapping workflows. Go/Restic minimums increased. Unknown YAML keys and malformed restore-list lines fail rather than being ignored.

## Gap-closing qualification

Default tests include the 40-case version/driver/running-state backup/restore matrix and native stream/retention/space regressions. These are command models. The separate `lxdintegration` and `storageintegration` tags are opt-in, with explicit disposable resource naming and required prerequisites. The legacy SFTP workflow checks payload recovery and a deliberately incomplete backup's nonzero CLI exit. The kernel-stream workflow measures actual full/delta sends and verifies recovered payloads while modeling LXD import. Neither skip nor modeled import qualifies a real LXD/backend pair. CI installs GNU tar in addition to Restic/zstd and validates workflow syntax with the configured disposable runner label.

To keep verification practical, use affected short tests during implementation and one combined `go test -race -coverprofile=... ./...` for full recovery checks. If a test-only assertion requires correction, rerun the affected short/race checks rather than repeating slow unchanged Restic scenarios. Preserve the initial result and correction in VALIDATION.md.
