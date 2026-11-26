# lxcer

A lightweight backup and recovery tool for LXD with multi-remote support, concurrent backups, Restic integration, Zstandard compression, Prometheus metrics and backup verification.

**Current release candidate** `v0.4.0-rc.1` is available for qualification from source. Live LXD backup and restore still need qualification on the target environment. [Maintained repository](https://github.com/muchezz/lxcer) · [Compatibility matrix](docs/COMPATIBILITY.md)

## Features

- Back up running and stopped LXD instances locally, through authenticated LXD remotes, or with a source-side SSH agent.
- Limit concurrency globally and per host, optionally compress archives with Zstandard, and write to one or more Restic destinations.
- Verify uploaded snapshots and retain staging on upload failure. Restore into a new stopped instance and validate recovery on an isolated LXD host.
- Export backup, recovery, and freshness metrics through the Node Exporter textfile collector.

## Install

Use Linux AMD64 or ARM64, an authenticated `lxc` client, and Restic **0.16.0 or later**. Use Zstandard **1.3.8 or later** for compression, SSH for agent mode, and GNU tar for native filesystem image reconstruction. Building from source requires Go **1.26 or later**.

From the repository root:

```sh
go build -trimpath -o ./lxcer .
install -Dm755 ./lxcer "$HOME/.local/bin/lxcer"
export PATH="$HOME/.local/bin:$PATH"
lxcer version
```

For release candidate builds, checksum verification, and side-by-side upgrades, see [builds and releases](docs/DEVELOPMENT.md).

## Quick start

On a disposable LXD host, start with the example configuration. Initialize the example repository only if it is new:

```sh
cp conf.yml lxcer.yml
restic -r ./restic-repo init
```

Set `LXCER_RESTIC_PASSWORD` in the backup user's environment to the repository password, or configure a protected `password_file` in `lxcer.yml`. Run from the same working directory so the example's relative repository and state paths resolve consistently.

```sh
lxcer validate -config lxcer.yml
lxcer capabilities -config lxcer.yml -local
lxcer backup -config lxcer.yml -local -container example
```

Replace `example` with the name of a disposable LXD instance.

## Configuration and remote backups

Set `inventory` for source hosts, `workers` for concurrency, `state_dir` for private staging and manifests, and `destinations` for Restic repositories. Destinations can be local paths or Restic backend addresses such as SFTP and object storage. Initialize repositories with Restic and set credentials for the backup account. Keep staging on a disk with enough space for archives and retained failures. [Configuration reference](docs/CONFIGURATION.md) · [Example configuration](examples/modern.yml)

Register direct LXD remotes with the backup user's `lxc` client. For example, use `lab` for a disposable remote. The second command backs up the sources in `inventory` using their configured worker limits:

```sh
lxcer backup -config lxcer.yml -remote-host lab -container example
lxcer backup -config lxcer.yml -concurrently
```

Use SSH agent mode when lxcer needs access to files on the source host. The agent uses its own configuration, credentials, state, and metrics. [Remote configuration](docs/CONFIGURATION.md#host-and-instance-policies) · [Application consistency](docs/APPLICATIONS.md)

## Verify and recover

A successful backup verifies its Restic data snapshots, expected paths, and catalog receipts. Enable `check` for repository structure checks or `read_data` to verify repository pack data. Find the job ID in the backup log or `lxcer list`, then use its manifest:

```sh
manifest=./lxcer-state/JOB_ID/manifest.json
lxcer verify -config lxcer.yml -manifest "$manifest"
lxcer restore-plan -config lxcer.yml -manifest "$manifest" -local -as example-restored
lxcer restore -config lxcer.yml -manifest "$manifest" -local -as example-restored
```

Use a free target name. A modern restore leaves the instance stopped. Run `recover` to inspect an interrupted job. If local manifests are lost, use `discover -legacy` to locate a receipt and `recover-catalog` to retrieve its manifest. [Recovery guide](docs/RESTORE.md)

## Monitoring

Set `metrics_file` to a `.prom` path in Node Exporter's textfile directory. Scrape Node Exporter with Prometheus, import the Grafana dashboard, and route alerts through Alertmanager for failed, overdue, missing, or interrupted backups. [Monitoring setup](docs/OPERATIONS.md#monitoring) · [Alert rules](monitoring/alerts.yml) · [Grafana dashboard](monitoring/grafana.json)

## CLI reference

Run `lxcer COMMAND -config FILE`. The default config is `conf.yml`. Commands that select one repository use `-destination NAME` when multiple destinations are configured. `lxcer -help` lists the flags.

| Command | Use |
|---|---|
| `version` | Print version, commit, and build date. |
| `validate` | Check YAML and password file permissions. |
| `capabilities` | Inspect LXD with `-local` or `-remote-host NAME`. |
| `backup` | Select a local host, remote, or configured inventory. Use `-container NAME` to limit selection. |
| `list` | Print local job manifests. |
| `discover` | List Restic data snapshots. Add `-legacy` to include receipts and older snapshots. |
| `verify` | Verify the recorded data snapshot and check for the receipt when present. Requires `-manifest FILE`. |
| `restore-plan` | Check repository and target constraints before importing. |
| `restore` | Recover from a manifest or catalog entry. Select a target and supply `-as NAME` or a restore list. |
| `restore-test` | Verify recovery on a separate remote with an isolated profile. |
| `recover-catalog` | Extract a manifest from `-snapshot ID` into a new `-target DIR`. |
| `adopt-legacy` | Verify and catalog an older archive with supplied instance metadata. |
| `recover`, `cleanup` | Preview recovery or cleanup. Add `-execute` to act on eligible jobs. |
| `retention` | Preview Restic retention for an explicit source and instance. Add `-execute` to forget eligible snapshots. |

## Migration and compatibility

Unversioned configuration and legacy `-a backup` and `-a restore` invocations remain supported, including original `.tar.zst` archives. Legacy restores with unversioned config start the instance unless you pass `-stopped`. Add `version: 1` when ready for modern defaults, and use the `cleanup` command for eligible lxcer jobs. [Migration options](docs/CONFIGURATION.md#legacy-configuration) · [Legacy recovery](docs/RESTORE.md)

**Verified:** the AMD64 CLI on Ubuntu 24.04, local Restic recovery, Zstandard archives, Prometheus textfile metrics, and LXD capability negotiation against disposable endpoints. The ARM64 CLI ran under QEMU. **Expected:** other reviewed Ubuntu and LXD combinations listed in the matrix. Qualify live exports, imports, storage drivers, and the intended Restic backend on disposable infrastructure before relying on this candidate. [Verified, expected, and unsupported combinations](docs/COMPATIBILITY.md)

## Attribution

This project originated as a fork of pavel1337/lxcer. This independently maintained fork focuses on improving backup reliability, recovery safety, observability, portability and operational usability. Credit for the original implementation belongs to its original author and contributors.

[Original lxcer repository](https://github.com/pavel1337/lxcer)

The original author authorized this fork to publicly redistribute its modified source and compiled binaries. The upstream project has no identified general license grant. [Project and dependency notices](NOTICE) · [Redistribution details](docs/DEVELOPMENT.md#redistribution)
