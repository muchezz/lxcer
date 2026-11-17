# lxcer

Backup, verify and restore LXD containers using LXC and Restic. Originally written by [pavel1337](https://github.com/pavel1337/lxcer); modernization developed in [muchezz/lxcer](https://github.com/muchezz/lxcer). The original Git history and module name are preserved.

The modernization replaces the original duplicated pipelines with locked, durable jobs. Running and stopped containers are selected consistently; failed required uploads produce a nonzero exit and retain recovery artifacts. Required Restic snapshots and tree paths are verified before cleanup. New restore commands create stopped instances and refuse collisions. Original flag-based restores with unversioned configuration retain automatic startup; `-stopped` opts out.

**Compatibility status:** automated command simulations cover LXD 3.0.3, 4.0, 5.0, 5.21, 6.0 and capability-based future behavior. No real LXD server/storage combination has yet been verified in this development environment. Real local Restic 0.16.4 and zstd 1.5.5 are exercised in isolated tests. Read [compatibility](docs/COMPATIBILITY.md) before adoption. This branch needs disposable-host qualification before production rollout.

## Build and install

Linux, Go 1.26 or later, authenticated `lxc`, Restic 0.16 or later, and optionally zstd 1.5 or later. Earlier Restic releases used by the original tool require upgrading for verified restores. No privileged daemon is installed.

```sh
go test ./...
go build -trimpath -o ./lxcer .
./lxcer version
./lxcer validate -config conf.yml
```

Build and test the local binary before installing a separately versioned copy under `/opt/lxcer`. This work does not replace `/usr/local/bin/lxcer`. Initialize repositories separately with Restic; lxcer never initializes or prunes production repositories automatically.

## Usage

```sh
# Credentials come from an environment variable, or a protected password_file.
export LXCER_RESTIC_PASSWORD='your-repository-password'
./lxcer backup -config conf.yml -local -container stopped-staging
./lxcer backup -config conf.yml -concurrently
./lxcer capabilities -config conf.yml -remote-host legacy
./lxcer list -config conf.yml
./lxcer discover -config conf.yml -destination primary
./lxcer verify -config conf.yml -manifest /private/state/lxcer-ID/manifest.json
./lxcer restore-plan -config conf.yml -container app -as app-dr -remote-host lab -destination primary
./lxcer restore -config conf.yml -container app -as app-dr -remote-host lab -destination primary
./lxcer cleanup -config conf.yml                  # preview only
./lxcer recover -config conf.yml -execute         # resume interrupted hooks, retain failed artifacts
```

Unversioned configurations retain the snapshot/image/zstd pipeline and `<instance>.tar.zst` archive names. `restore_restic_repo` still selects the restore repository; old flag-based restores can discover original archives without a manifest or manual adoption. Modern versioned configuration enables the new strategy defaults.

`-a backup`, `-config`, `-local`, `-container`, `-remote-host`, `-concurrently`, `-restore-list` and `-as` remain available. `-cleanup` is rejected because its original implementation deleted unrelated resources. All command failures return 1; malformed CLI arguments return 2. Partial success returns 1 with `PartiallyCompleted` in the manifest.

## Operations documentation

- [Architecture and baseline audit](docs/ARCHITECTURE.md), [baseline](docs/BASELINE.md)
- [Configuration, migration, strategies and multi-host operation](docs/CONFIGURATION.md)
- [LXD and storage compatibility](docs/COMPATIBILITY.md)
- [Restoration and disaster recovery](docs/RESTORE.md)
- [Application consistency and legacy GitLab example](docs/APPLICATIONS.md)
- [Security, monitoring, troubleshooting and performance](docs/OPERATIONS.md)
- [Testing, releases and upstream contribution](docs/DEVELOPMENT.md)
- [Validation report](docs/VALIDATION.md)

No license file was present in the original repository. Preserve attribution; obtain the author's explicit license terms before redistributing releases. Local development is unaffected.
