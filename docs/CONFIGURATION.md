# Configuration and strategies

See [modern.yml](../examples/modern.yml). YAML fields are strict; unknown fields, unsupported versions, invalid worker counts/names/hooks and conflicting password sources fail validation. Configuration version defaults to 1 for old files. Unversioned files keep the original snapshot/image pipeline and zstd default; explicit `version: 1` opts into automatic strategy selection and the new compression default. Explicit policy/compression settings always take precedence. `LXCER_STATE_DIR` overrides staging/catalog location; `LXCER_PASSWORD_FILE` overrides destination password sources. State directories must be mode 0700. The minimum free-byte threshold is checked before work; LXD also needs enough server-side space for its export/snapshot. Mid-operation ENOSPC failures propagate and preserve staging.

Legacy `hosts: [remote]`, `blacklist`, `local_workers`, `backup_restic_repos: [{path, password}]`, and `restore_restic_repo` remain decodable. `local_workers` takes precedence over workers for migration. Convert plaintext password to `password_env` or `password_file`; named `destinations` replace old repositories. `restore_restic_repo` still selects the restore repository when `-destination` is omitted, using its original credentials. The old check comments are superseded by per-destination verification. The old destructive `-cleanup` flag is rejected.

## Strategy selection

| Strategy | Behavior | Constraints |
|---|---|---|
| auto | Full instance export if client command and server extension agree; otherwise snapshot/image | Records selected strategy; no fallback after an operation fails |
| full | Portable `lxc export`, including instance snapshots according to LXD behavior | Server `container_backup`; recoverability on older targets depends on archive format |
| snapshot | Job snapshot, image publication and remote image export; full config in sidecar | Containers only; unified images; does not preserve snapshot history as an instance backup |
| native | Restic directly scans explicit source filesystem paths and native_root | Source-side execution; stopped instance or explicit quiesce/resume hooks; automatic image reconstruction when metadata/privileges permit, or offline filesystem restore |
| storage-incremental | Read-only Btrfs/ZFS snapshots and full/parent-relative send streams | Explicit source/root, source-side execution, dependency-preserving retention; see STORAGE_STREAMS.md |
| incremental | Persistent mirror with initial relay copy, then capability-gated `lxc copy --refresh`, portable export at mirror | Requires source refresh capability, modern export-capable mirror and ownership marker; LXD chooses transfer optimizations |

Incremental refers to the source-to-mirror refresh. It still exports a full archive and scans/uploads that archive through Restic at the mirror. Actual wire reduction depends on shared snapshots, drivers and LXD migration support. It is not a standalone ZFS/Btrfs delta archive chain. No strategy silently turns a failed incremental operation into a full copy. Initial copy may be expensive. Mirrors remain stopped with autostart disabled; do not use them as production instances.

`compression: zstd` compresses the artifact with one CPU per job; `none` leaves the LXD-selected compression intact. It does not override server default compression in versions without command support. Restic's own repository compression is independent. Staging uses `state_dir`; it is retained on failure. Loaded configurations default to a 32 GiB monitored staging budget (`max_staging_bytes`); raise it deliberately for larger archives. `require_pool_space: true` makes source capacity discovery mandatory. See STORAGE_STREAMS.md for quotas, source sizing and stream limitations. Portable archives avoid backend-specific optimized blob dependencies. Streaming directly into multiple verified destinations is not implemented; use source-side native backups to avoid archive copies when appropriate.

## External data

Expanded disk devices distinguish root, additional managed volumes and bind mounts. Root is covered by instance export. Additional storage fails preflight unless captured or explicitly acknowledged with `allow_excluded: true`. Exclusions are logged and recorded and are never presented as included.

Set `backup_volumes: true` to export attached custom volumes when `custom_volume_backup` is advertised. Each export has its own checksum, size and artifact path; failure aborts the job. This requires compatible client volume-export commands. Restore uses new volume names (`restored-instance` + original volume name), detects collisions and requires `-restore-volumes`; failed restores retain newly created volumes for diagnosis. Cross-version custom volume formats require independent qualification.

For a host bind mount, use `paths: [/srv/gitlab-data]` on the source agent. The manifest marks an attachment included only if its absolute source lies under an explicitly backed-up filesystem path. Filesystem paths may accompany a full/image export or form native backups. If any live paths are scanned, quiescence remains in effect until all destinations finish. For native root backups set `native_root` explicitly; lxcer never guesses LXD's private storage layout or mounts inactive storage volumes. ZFS/Btrfs/LVM datasets must already be accessible through a consistent mounted filesystem path.

## Host and instance policies

Global policy is overlaid by host policy and then instance policy. Nonempty strategy/root/mirror and explicitly present paths/hook lists override; safety opt-ins (`allow_excluded`, `backup_volumes`) inherit once enabled. Use an empty hook list to clear inherited hooks. Missing host workers defaults to one. `-concurrently` enables configured global concurrency; sequential legacy invocation uses one global worker.

Authenticated LXD remote mode exports directly from the source without first copying the container to the controller. Preconfigure client certificate trust/PKI with LXC; lxcer never disables TLS verification. For legacy relays choose incremental with an explicit mirror where supported, or the snapshot image adapter. SSH mode uses `ssh: alias` plus absolute `remote_config`, executes the reviewed `agent_binary` (or the legacy `lxcer` PATH default) with `-local` and a stable `-source-id`, and leaves credentials/catalog on that source host. A controller's destination definitions are not sent to agents. Remote agent code/config must match the controller deployment. SSH jobs consume a global slot; each agent separately enforces its own per-instance limits.

Timeout applies to the command and each job; retries apply to Restic operations with bounded backoff, including repository lock contention. lxcer never forcibly unlocks a live Restic repository. LXD mutations are not blindly retried because command interruption may have created resources. `bandwidth_kib` limits Restic upload traffic per destination/job, not aggregate LXD/SSH migration bandwidth. Configure LXD `rsync.bwlimit`, SSH/network QoS or a source-side agent to control those transfers. Do not change production LXD server settings automatically.

Schedules use the supplied systemd timer/service examples. They run one-shot commands; lxcer has no always-running scheduler or privileged daemon. Adjust service path, user, source access and timeout before installation.
