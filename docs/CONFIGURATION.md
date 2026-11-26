# Configuration and strategies

See [modern.yml](../examples/modern.yml). YAML fields are strict; unknown fields, unsupported versions, invalid worker counts/names/hooks and conflicting password sources fail validation. Configuration version defaults to 1 for old files. Unversioned files keep the original snapshot/image pipeline and zstd default; explicit `version: 1` opts into automatic strategy selection and the new compression default. Explicit policy/compression settings always take precedence. `LXCER_STATE_DIR` overrides staging/catalog location; `LXCER_PASSWORD_FILE` overrides destination password sources. State directories must be mode 0700. The minimum free-byte threshold is checked before work; LXD also needs enough server-side space for its export/snapshot. Mid-operation ENOSPC failures propagate and preserve staging.

## Legacy configuration

Keep the original configuration to use `hosts`, `blacklist`, `local_workers`, `backup_restic_repos`, and `restore_restic_repo` with the legacy CLI. `local_workers` takes precedence over `workers`. When `-destination` is omitted on restore, `restore_restic_repo` selects the repository and its configured credentials.

To migrate, use the [sample configuration](../conf.yml) as a starting point. Move sources from `hosts` to `inventory`, name each repository in `destinations`, and replace plaintext passwords with `password_env` or `password_file`. Set `version: 1` for modern defaults. Use per-destination `check` or `read_data` for repository checks. Use `lxcer cleanup` to preview eligible job cleanup. The legacy `-cleanup` flag is rejected.

## Strategy selection

| Strategy | Behavior | Constraints |
|---|---|---|
| auto | Full instance export if client command and server extension agree; otherwise snapshot/image | Records selected strategy; no fallback after an operation fails |
| full | Portable `lxc export`, including instance snapshots according to LXD behavior | Server `container_backup`; recoverability on older targets depends on archive format |
| snapshot | Job snapshot, image publication and remote image export; full config in sidecar | Containers only; unified and split images; does not preserve snapshot history as an instance backup |
| native | Restic directly scans explicit source filesystem paths and native_root | Source-side execution; stopped instance or explicit quiesce/resume hooks; automatic image reconstruction when metadata/privileges permit, or offline filesystem restore |
| storage-incremental | Read-only Btrfs/ZFS snapshots and full/parent-relative send streams | Explicit source/root, source-side execution, dependency-preserving retention; see [storage streams](STORAGE_STREAMS.md) |
| incremental | Persistent mirror with initial relay copy, then capability-gated `lxc copy --refresh`, portable export at mirror | Requires source refresh capability, modern export-capable mirror and ownership marker; LXD chooses transfer optimizations |

Incremental refers to the source-to-mirror refresh. It still exports a full archive and scans/uploads that archive through Restic at the mirror. Actual wire reduction depends on shared snapshots, drivers and LXD migration support. It is not a standalone ZFS/Btrfs delta archive chain. No strategy silently turns a failed incremental operation into a full copy. Initial copy may be expensive. Mirrors remain stopped with autostart disabled; do not use them as production instances.

`compression: zstd` compresses the artifact with one CPU per job; `none` leaves the LXD-selected compression intact. It does not override server default compression in versions without command support. Restic's own repository compression is independent. Staging uses `state_dir`; it is retained on failure. Loaded configurations default to a 32 GiB monitored staging budget (`max_staging_bytes`); raise it deliberately for larger archives. `require_pool_space: true` makes source capacity discovery mandatory. See [storage streams](STORAGE_STREAMS.md) for quotas, source sizing and stream limitations. Portable archives avoid backend-specific optimized blob dependencies. Streaming directly into multiple verified destinations is not implemented; use source-side native backups to avoid archive copies when appropriate.

## External data

Expanded disk devices distinguish root, additional managed volumes and bind mounts. Root is covered by instance export. Additional storage fails preflight unless captured or explicitly acknowledged with `allow_excluded: true`. Exclusions are logged and recorded and are never presented as included.

Set `backup_volumes: true` to export attached custom volumes when `custom_volume_backup` is advertised. Each export has its own checksum, size and artifact path; failure aborts the job. This requires compatible client volume-export commands. Restore uses new volume names (`restored-instance` + original volume name), detects collisions and requires `-restore-volumes`; failed restores retain newly created volumes for diagnosis. Cross-version custom volume formats require independent qualification.

For a host bind mount, use `paths: [/srv/example-data]` on the source agent. The manifest marks an attachment included only if its absolute source lies under an explicitly backed-up filesystem path. Filesystem paths may accompany a full/image export or form native backups. If any live paths are scanned, quiescence remains in effect until all destinations finish. For native root backups set `native_root` explicitly; lxcer never guesses LXD's private storage layout or mounts inactive storage volumes. ZFS/Btrfs/LVM datasets must already be accessible through a consistent mounted filesystem path.

## Host and instance policies

Global policy is overlaid by host policy and then instance policy. Nonempty strategy/root/mirror and explicitly present paths/hook lists override; safety opt-ins (`allow_excluded`, `backup_volumes`) inherit once enabled. Use an empty hook list to clear inherited hooks. Missing host workers defaults to one. `-concurrently` enables configured global concurrency; sequential legacy invocation uses one global worker.

Authenticated LXD remote mode exports directly from the source without first copying the container to the controller. Preconfigure client certificate trust/PKI with LXC; lxcer never disables TLS verification. For legacy relays choose incremental with an explicit mirror where supported, or the snapshot image adapter. SSH mode uses `ssh: alias` plus absolute `remote_config`, executes the reviewed `agent_binary` (or the legacy `lxcer` PATH default) with `-local` and a stable `-source-id`, and leaves credentials/catalog on that source host. A controller's destination definitions are not sent to agents. Remote agent code/config must match the controller deployment. SSH jobs consume a global slot. With `-concurrently`, each agent applies its own worker limits.

Timeout applies to the whole command, including concurrent jobs; retries apply to Restic operations with bounded backoff, and Restic waits up to a minute per operation for repository locks. Processes sharing a state directory coordinate exclusive repository checks and retention with concurrent uploads. lxcer never forcibly unlocks a live Restic repository. LXD mutations are not blindly retried because command interruption may have created resources. `bandwidth_kib` limits Restic upload traffic per destination/job, not aggregate LXD/SSH migration bandwidth. Configure LXD `rsync.bwlimit`, SSH/network QoS or a source-side agent to control those transfers. Do not change production LXD server settings automatically.

Schedules use the supplied systemd timer/service examples. They run one-shot commands; lxcer has no always-running scheduler or privileged daemon. Adjust service path, user, source access and timeout before installation.

## Option reference

| Scope | Keys |
|---|---|
| Configuration and identity | `version`, `state_dir`, `host_id`, `blacklist` |
| Runtime limits | `workers`, `timeout`, `retries`, `bandwidth_kib`, `compression`, `min_free_bytes`, `max_staging_bytes`, `require_pool_space` |
| Monitoring | `metrics_file` |
| Sources | `inventory[].name`, `workers`, `ssh`, `agent_binary`, `remote_config`, `policy`, `instances` |
| Destinations | `destinations[].name`, `path`, `password`, `password_file`, `password_env`, `optional`, `check`, `read_data`, `keep_last`, `keep_daily`, `keep_weekly` |
| Backup policy | `strategy`, `paths`, `allow_excluded`, `backup_volumes`, `native_root`, `mirror`, `storage_source`, `snapshot_dir`, `max_chain`, `incremental_fallback`, `hooks` |
| Lifecycle hooks | `hooks.pre`, `quiesce`, `resume`, `post`. Each entry takes `argv`, `timeout`, and `optional`. |
| Legacy inputs | `hosts`, `local_workers`, `backup_restic_repos`, `restore_restic_repo` |

The default command timeout is one hour, the global worker count is one, and Restic retries and upload throttling are disabled until set. The minimum free-space threshold defaults to 1 GiB. Set a space threshold to zero to disable that limit. Space monitoring samples growth, so hard byte limits require filesystem quotas. Relative paths are resolved from the command's working directory. If you provide an explicit password source, it takes priority over inherited Restic password settings. Otherwise Restic inherits environment settings for its credentials and backend.
