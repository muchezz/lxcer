# Architecture

`main.go` parses commands and legacy flags, handles signals and nonzero exit status. `config.go` strictly decodes versioned YAML, validates names and policies, and migrates legacy repository/worker/host keys in memory. `lxd.go` detects client commands and server API extensions independently, selects strategies, inventories expanded storage devices and detects pool drivers.

`engine.go` schedules hosts with a global semaphore and per-host semaphores. Each instance acquires a Linux flock keyed by host/name. `runner.go` uses argv execution, contexts, a separate process group, bounded output and suppressed subprocess diagnostics. There is no shell execution in local workflows. SSH agent mode quotes the remote command and uses SSH config aliases with strict host key checking.

`manifest.go` creates private job directories, atomic fsynced JSON manifests and a bounded state machine. `restic.go` backs up data and metadata, extracts the exact JSON summary snapshot ID, verifies repository identity and every expected tree path, optionally checks repository data, and implements scoped retention. `restore.go` restores into private staging, verifies archive hashes, imports stopped instances, replaces source profile dependencies with explicit devices, validates ownership/state, and performs isolated restore tests. `catalog.go` handles catalog lookup and optional legacy adoption. `legacy.go` preserves original flag/config/archive restores without requiring a catalog, while adding verified extraction and collision protection. Effective profile configuration is captured in sidecars; legacy adoption preserves the original snapshot timestamp. `metrics.go` reconstructs Prometheus textfile metrics from the durable catalog.

## Job lifecycle

Pending -> Running -> SnapshotCreated (image strategy) -> Exported -> Uploaded -> Verified. Full/native/refresh skip SnapshotCreated. Failures before successful verification terminate as Failed or PartiallyCompleted. Optional destination failure is partial completion with a nonzero process exit. A later cleanup/post-hook/receipt failure can downgrade Verified to PartiallyCompleted. Invalid normal transitions are rejected. Final manifests record timestamps, host, instance state/configuration, server/client capabilities, driver, strategy, size/hash, attachment coverage, per-repository snapshot/receipt IDs, verification level, errors, cleanup and restore-test results.

Snapshot names and image aliases are random job IDs. Resource intent is saved before creation. Export archives and sidecars reside in private job staging. Crash recovery obtains the source lock, replays persisted resume hooks where needed and marks interrupted jobs failed. It never guesses that a partially uploaded artifact is recoverable. Failed exports/uploads retain staging and job-owned LXD snapshots for diagnosis; the operator verifies or repeats the job before manually removing failed resources.

## Remote catalog and verification levels

Each data snapshot contains the archive/filesystem paths and an export-time manifest. A second `lxcer-receipt` snapshot contains verified destination IDs; tags include job, source host and instance. The receipt necessarily cannot contain its own final ID. Local final manifests record receipt IDs and cleanup status. Recover receipts or data sidecars directly with Restic after local state loss. Receipt backups are separately verified; a required receipt failure retains artifacts.

Metadata verification confirms snapshot identity, declared paths and corresponding tree nodes. `check: true` also validates repository structure. `read_data: true` reads repository pack data. Restore verifies restored data and the recorded artifact SHA256. Only a completed isolated restore test sets restore-test success. None of these checks alone proves application-level recovery without an appropriate health command.

## Cleanup boundaries

Only Verified jobs with verified required destinations and receipts are eligible. Cleanup removes exact job snapshots, job image aliases and files directly within the matching job directory, preserving the manifest. Content-addressed images are deliberately retained because another job/user may reference the same fingerprint; global image deletion is prohibited. Persistent refresh mirrors are never normal temporary cleanup targets. Cleanup is idempotent and recovery can retry it. Failed resources are listed and retained, rather than automatically aged out.

Locks coordinate processes sharing a state directory. Multiple controllers must share that directory or run the source-side SSH agent using a single source state directory; independent state directories do not form a distributed lock service. Instance aliases pointing at the same source must not be scheduled as distinct hosts.
