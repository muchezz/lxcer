# Security, monitoring and operations

## Trust boundaries

LXD socket membership is effectively host administration. Use a dedicated service identity with only the required LXD project/remote access. Filesystem-native backup may need root to read all files/metadata; grant only source-path read access where possible. Restore may need ownership and extended-attribute privileges. Do not mount raw production volumes or grant device management automatically. SSH credentials and LXD trust are configured by the operator; strict host-key checks are enforced in agent mode. Root-only isolated profiles and separate test infrastructure must be provisioned deliberately.

Passwords can come from an environment variable, a protected regular password file (0600 or stricter), or legacy plaintext YAML. Multiple explicit sources fail validation. Inherited Restic/SFTP/rclone/object-storage environment variables are passed to subprocesses. Use SSH identities, AWS profiles or short-lived credentials as appropriate. Repository strings/credentials are not logged; subprocess stdout/stderr failures are suppressed to avoid leaking secrets. Hook definitions, instance metadata and resume argv live in private manifests and encrypted repositories, so avoid putting secrets in argv. Configurations/manifests are trusted administrative input; protect them from untrusted writes.

Every job directory is 0700; manifests are atomic 0600 files. Names, state ownership, native source paths, restored instance names and artifact parent directories are validated. Subprocesses receive argument arrays. The only remote shell boundary is SSH agent invocation with single-quote escaping and validated SSH aliases. Signals cancel command process groups. Do not run concurrent controllers with independent source-state directories.

There is no repository-wide prune or automatic unlock. `retention` requires an explicit destination, source host and instance and defaults to dry-run. `-execute` forgets only snapshots with all three data/source tags under the configured keep-last/daily/weekly policy. Receipt metadata is retained. Storage reclamation by Restic prune is a separate explicitly authorized administrative task. Private failed staging is retained indefinitely until reviewed; alert on disk space and follow the job manifest rather than filesystem age alone.

## Monitoring

Set `metrics_file` to an existing node_exporter textfile collector directory. Metrics are rebuilt at job start/completion and recovery command completion from retained manifests. An interrupted process cannot update its own gauge; run recovery and alert on stale metrics. `lxcer_backup_jobs_running` counts unfinished manifests in the latest atomic metrics refresh; it is not a continuously sampled HTTP endpoint. Host and instance are the only labels; job IDs and snapshot IDs are excluded to bound cardinality by inventory size. Keep historical manifests to preserve failure counters/last-success values. External catalog deletion resets those reconstructed counters.

[Alert rules](../monitoring/alerts.yml) include failed/missing/stale backups, verification and repeated failures, restore failures and excessive duration. Add an expected-inventory series for per-instance absence alerts and node_exporter textfile staleness alarms. A global absence alert cannot enumerate instances never seen before. Import the [Grafana dashboard](../monitoring/grafana.json) and choose your Prometheus datasource. Configure the systemd service to surface nonzero exits to your alert transport. JSON logs default to info; `-log-level` and `-json-log=false` select verbosity/format. Logs identify job, host, instance, operation and failures without repository secrets.

## Performance

Limit global and host workers to match snapshot/export capacity and available staging space. Zstd uses one CPU per job to avoid workers each saturating all CPUs. Portable archive exports require full staging plus optional compressed output; reserve both. Source-side native backups avoid archive materialization and preserve file-level Restic deduplication, but filesystem mounting and consistent capture are the operator's responsibility. Refresh mirrors reduce source transfer only where LXD can use an efficient migration path; they do not remove full export cost. Use a fast private state disk and monitor both its free space and LXD pool capacity.

`bandwidth_kib` is per Restic upload; multiple workers/destinations multiply throughput. Use network-level QoS for aggregate limits or LXD migration traffic. Strong `read_data` verification reads the whole repository and can dominate runtime; schedule it periodically via `restic check --read-data` in addition to normal path verification if running it per job is too expensive. Keep timeouts long enough for the largest legitimate export while still detecting stalls.

## Troubleshooting

- **No containers matched:** inspect the exact authenticated host, `-container` and blacklist. Stopped instances are eligible.
- **Capability discovery failed:** ensure this user's LXC config has authenticated remote access and a compatible `query` command. No fallback bypasses authentication.
- **Export failed:** inspect job state and source pool space. Reproduce only the specific failing operation in a disposable environment. Subprocess diagnostics are intentionally suppressed; lxcer retains state/resources and exit context, not raw potentially secret stderr.
- **Required repository failed:** restore/check its recorded snapshot ID with secure Restic tooling. The archive stays in the job directory; fix credentials/locks/network and repeat backup. No failed job cleanup runs automatically.
- **Lock already held:** another process owns the source job. Kernel flock releases on process exit; do not delete lock files while workers run.
- **Crash during quiesce:** run `recover` preview then `recover -execute`, inspect resume outcomes and application health before rescheduling.
- **Unexpected image disk usage:** legacy publication retains content-addressed images after removing job aliases. Inspect image ownership/references manually; never globally delete images.
- **Restore collision:** choose a new instance/volume name. lxcer has no destructive overwrite shortcut.
- **External disk restore rejected:** restore filesystem/managed volume dependencies independently, review target devices and use a root-only isolated restore test. The tool does not silently reconnect production storage.
- **Stale source-side agent metrics:** collect the agent's own metrics/logs; the controller only receives SSH execution status.
