# Application consistency and examples

Running-instance snapshots are generally crash-consistent. Application consistency requires an audited application backup or explicit write quiescence covering every related dataset. Hooks run inside the source instance using `lxc exec` argv, with per-hook timeouts and optional-failure policy. Required hook failures abort the job. Resume intent is saved before quiesce and replayed using a fresh timeout after failures or cancellation. Hooks must be idempotent; recovery after SIGKILL uses the saved resume argv.

`pre` should create an application-native dump into a backed-up path. `quiesce`/`resume` can stop and resume writers only when that behavior is explicitly configured and operationally authorized. Post hooks run after data capture and required upload verification but before receipt finalization/cleanup. Native/direct path capture keeps quiescence active through scanning/upload. Do not use `optional: true` on consistency-critical hooks. Scripts must fail on errors and securely protect their dumps/credentials.

Recommended hook script contracts (scripts are application- and installation-specific; lxcer does not ship unsafe generic stop/start scripts):

| Profile | Pre hook script should do | Restore health script should do |
|---|---|---|
| PostgreSQL | Run `pg_dump` per DB plus `pg_dumpall --globals-only` using peer auth or a protected pgpass; write dumps atomically, check every exit status | Initialize a compatible server, import roles and DB dumps, check DB counts and application queries |
| MySQL/MariaDB | Use vendor-matched `mysqldump --single-transaction` including routines/events/triggers; handle nontransactional tables with an explicitly authorized lock or stop | Import into compatible server and validate tables, grants and application queries |
| MongoDB | Use a supported replica-set-consistent `mongodump --oplog` workflow when applicable; otherwise coordinate writers and sharded-cluster consistency explicitly | Restore dump/oplog with compatible tooling; check collections, indexes and application reads |
| GitLab | Use the installed version's `gitlab-backup create`; separately back up configuration, secrets and all repository/object-storage dependencies | Install matching GitLab version/edition, restore config/secrets, restore backup, run GitLab checks and repository verification |

A pre hook can use `argv: ["/usr/local/sbin/lxcer-postgresql-dump"]` with `timeout: 30m`; analogous scripts apply to the other rows. Passwords belong in protected files/service identities, never hook argv. If dumps are complete recovery artifacts, a snapshot can remain crash-consistent while application recovery uses the dumps. If recovering raw live application files, configure quiesce/resume and qualify that workflow separately.

## LXD 3.0.3 running GitLab, backup server and Hetzner Storage Box

Assume `legacy` is an authenticated LXC remote for a 3.0.3 source; `gitlab` is running with `/srv/gitlab-data` bound at `/var/opt/gitlab`. A separate backup server runs lxcer. A stopped staging container is also selected (it is not filtered out). No service is stopped by the configuration below unless your explicitly installed scripts do so.

Because the bind mount is source-host data, run a source-side agent through the backup server's SSH inventory. Its private state/staging directory must have sufficient space. The controller configuration:

```yaml
version: 1
workers: 2
inventory:
  - name: legacy-agent
    ssh: legacy-agent
    agent_binary: /opt/lxcer/lxcer
    remote_config: /etc/lxcer/legacy-agent.yml
# All jobs use source-side agents; repository credentials live only on agents.
```

Configure the SSH alias with a dedicated identity and pinned host key. Install the same reviewed binary on the source agent. On that source, `/etc/lxcer/legacy-agent.yml`:

```yaml
version: 1
state_dir: /srv/lxcer-private
workers: 1
compression: zstd
timeout: 4h
inventory:
  - name: local
    workers: 1
    policy:
      strategy: snapshot
    instances:
      gitlab:
        paths: [/srv/gitlab-data]
        hooks:
          pre:
            - argv: [/usr/local/sbin/lxcer-gitlab-backup]
              timeout: 2h
          quiesce:
            - argv: [/usr/local/sbin/lxcer-gitlab-quiesce]
              timeout: 5m
          resume:
            - argv: [/usr/local/sbin/lxcer-gitlab-resume]
              timeout: 5m
      stopped-staging:
        strategy: snapshot
destinations:
  - name: storagebox
    path: sftp:storagebox:/home/lxcer
    password_file: /etc/lxcer/restic-password
    check: true
    keep_daily: 14
    keep_weekly: 8
```

`storagebox` is an SSH config alias for the Storage Box endpoint; configure its assigned user and port (commonly 23) from the account, a pinned key and dedicated key authentication. Do not put the account password or private key in YAML. Use Restic's SFTP repository syntax and initialize it separately.

The pre script must finish GitLab's version-specific backup command successfully, secure its output and capture `/etc/gitlab` including `gitlab.rb` and `gitlab-secrets.json`. Those paths are in the root image unless separately mounted; inspect expanded devices and add any host source paths. GitLab's native backup does not automatically cover all external object storage/registry dependencies; include those independently. The quiesce/resume scripts must coordinate GitLab writers for raw bind/root capture and be idempotent. Their deployment is explicit authorization for the service impact; validate first on an isolated clone. Source-side scanning may keep writers quiesced during a long offsite upload, so prefer application dumps plus a qualified local consistent staging path for large installations.

Run the controller's backup command with `-concurrently`. The source agent includes both running GitLab and stopped staging, publishes unique snapshots/images using the legacy adapter, captures explicit bind data directly with Restic, verifies data snapshot paths and receipt before cleanup, and retains failure artifacts. No full container is copied to the backup server. The backup server orchestrates and monitors SSH status; detailed manifests/metrics remain on the source agent. A direct LXD-remote-only backup would reject the excluded bind mount; `allow_excluded` is not a substitute for protecting it.

Recover the root image into an isolated stopped staging instance on a separate compatible host, restore bind data into a new filesystem target with Restic, review/remap the external attachment, then use the matching GitLab restore procedure and validate databases/repositories/secrets before changing DNS or starting production services. The automatic root-only test intentionally excludes production bind mounts. LXD 3.0.3 command/storage behavior remains unverified until the disposable matrix passes; this is a concrete deployment configuration, not a claim of real-host qualification.
