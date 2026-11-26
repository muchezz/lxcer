# Security, monitoring and operations

## Trust boundaries

LXD socket membership is effectively host administration. Use a dedicated service identity with only the required LXD project/remote access. Filesystem-native backup may need root to read all files/metadata; grant only source-path read access where possible. Restore may need ownership and extended-attribute privileges. Do not mount raw production volumes or grant device management automatically. SSH credentials and LXD trust are configured by the operator; strict host-key checks are enforced in agent mode. Root-only isolated profiles and separate test infrastructure must be provisioned deliberately.

Passwords can come from an environment variable, a protected regular password file (0600 or stricter), or legacy plaintext YAML. Multiple explicit sources fail validation. Inherited Restic/SFTP/rclone/object-storage environment variables are passed to subprocesses. Use SSH identities, AWS profiles or short-lived credentials as appropriate. Repository strings/credentials are not logged; subprocess stdout/stderr failures are suppressed to avoid leaking secrets. Hook definitions, instance metadata and resume argv live in private manifests and encrypted repositories, so avoid putting secrets in argv. Configurations/manifests are trusted administrative input; protect them from untrusted writes.

Every job directory is 0700; manifests are atomic 0600 files. Exported metric files are 0644 so node_exporter can read them; they contain only monitoring metadata. Names, state ownership, native source paths, restored instance names and artifact parent directories are validated. Subprocesses receive argument arrays. The only remote shell boundary is SSH agent invocation with single-quote escaping and validated SSH aliases. Signals cancel command process groups. Do not run concurrent controllers with independent source-state directories.

Staging budgets and source-pool preflight are described in STORAGE_STREAMS.md. Kernel send/receive, owner/ACL normalization and image import require appropriate privileges and dedicated scratch capacity. Failed receivers remain journaled for review. New upload/capture/staging/chain-length gauges use bounded source/destination labels.

There is no repository-wide prune or automatic unlock. `retention` requires an explicit destination, source host and instance and defaults to dry-run. `-execute` forgets only snapshots with all three data/source tags under the configured keep-last/daily/weekly policy. Receipt metadata is retained. Storage reclamation by Restic prune is a separate explicitly authorized administrative task. Private failed staging is retained indefinitely until reviewed; alert on disk space and follow the job manifest rather than filesystem age alone.

## Monitoring

Use a persistent private `state_dir` and configure one scheduled writer per `metrics_file`. Have concurrently running lxcer commands that share a file also share the same state directory. Writes use a shared lock, a temporary file and an atomic rename. The backup account needs write access to the collector directory. Node Exporter needs read and traverse access, and reads the resulting 0644 file. Keep separate files on separate scrape targets, or use a single state and file per node so metric label sets stay unique. Collect source-side metrics from SSH agents on their own scrape targets. The controller exports its aggregate command result.

Set `metrics_file: /var/lib/node_exporter/textfile_collector/lxcer.prom` in lxcer and run Node Exporter with `--collector.textfile.directory=/var/lib/node_exporter/textfile_collector`. No metrics daemon or Pushgateway is required. [Node Exporter textfile collector](https://github.com/prometheus/node_exporter#textfile-collector)

### What to alert on

- **Failed command:** `lxcer_backup_run_success == 0` covers a completed backup invocation, including failures before instance discovery, configuration prerequisites checked by the backup command, and SSH agent failures. Config parsing or a failure to write the metrics file may leave only the service exit status and stale metrics.
- **Failed instance backup:** `lxcer_backup_success == 0` uses the newest completed attempt. Active work leaves the last result available. An attempt counts as successful after all required data and catalog receipts are verified and the job finishes without errors. The separate `lxcer_backup_required_destinations_verified` metric can show that a failed attempt still has recoverable required copies.
- **Overdue:** `time() - lxcer_backup_last_success_timestamp_seconds` measures age since the last successful backup finished. Failed, interrupted and running jobs preserve that timestamp. A zero value means the instance has completed an attempt but has no recorded success. The command completion timestamp also detects a stopped schedule, even when instance metrics are still being served from disk.
- **Missing:** keep a scrape target for each scheduled writer. For per-instance coverage, place an inventory file such as `lxcer_expected.prom` in the same collector directory, with a line such as `lxcer_backup_expected{host="lab",instance="database"} 1` for each expected instance. Update it when retiring or adding an instance. A missing expected instance or a zero last-success timestamp means there is no recorded successful backup.
- **Interrupted:** the running and interrupted gauges use job locks when rebuilding the textfile. After a process is killed the file remains unchanged until another update, such as `recover -execute`. Alert on command completion age and monitor scheduler exits as well.
- **Space and verification:** duration and staging peak describe the latest completed attempt. Artifact bytes sum the main artifact, split image parts and managed-volume archives when present. Files streamed directly from the source have no staged artifact size. Upload values are Restic's packed bytes per destination. Verification failures and job failures are counters reconstructed from local history. Deleting that history removes its metrics and can reset counters or freshness.

Metrics label instance series by source host and LXD instance, with destination aliases on upload samples. The command metrics are per scrape target. Repository URLs, paths, credentials and job IDs are excluded. Prometheus's default scrape labeling renames the instance label from the file to `exported_instance`; `instance` identifies the Node Exporter endpoint. [Prometheus scrape configuration](https://prometheus.io/docs/prometheus/latest/configuration/configuration/)

### Prometheus, Grafana and Alertmanager

Load [alerts.yml](../monitoring/alerts.yml) as a rule file and use a dedicated scrape job for Node Exporters that run lxcer. Merge these settings into your existing Prometheus configuration:

```yaml
rule_files:
  - /etc/prometheus/rules/lxcer.yml
scrape_configs:
  - job_name: lxcer-node
    static_configs:
      - targets: [backup-host.example:9100]
alerting:
  alertmanagers:
    - static_configs:
        - targets: [alertmanager.example:9093]
```

Adjust the 26-hour thresholds for your backup schedule. Use Alertmanager's existing route or match the `Lxcer` alert names and `severity` labels to route critical alerts to your paging receiver and warnings to your operations receiver. Group by `alertname`, `job`, and `instance`. In Grafana, import [grafana.json](../monitoring/grafana.json) and select the Prometheus data source. Use `host` and `exported_instance` to filter LXD instances. [Prometheus alert rules](https://prometheus.io/docs/prometheus/latest/configuration/alerting_rules/) · [Alertmanager routing](https://prometheus.io/docs/alerting/latest/configuration/#route)

The qualification checked the rule syntax with the Prometheus versions shipped by Ubuntu 18.04 through 26.04. It also checked a real Node Exporter 1.7.0 scrape. Use Prometheus 2.45.3 or a newer promtool for the metrics validation command below. See the [compatibility matrix](COMPATIBILITY.md).

Validate before loading the rules:

```sh
promtool check metrics < /var/lib/node_exporter/textfile_collector/lxcer.prom
promtool check rules monitoring/alerts.yml
promtool check config /etc/prometheus/prometheus.yml
```

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
