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

## Back up an instance with a bind mount

An LXD export covers the root disk. To capture a host directory mounted into a running instance, add it to the instance's `paths` and run lxcer on the source with quiesce and resume hooks. For example, the following source configuration captures an application instance and the data in `/srv/example-data`. Replace the instance name, path, scripts, and Restic destination with your own values.

```yaml
version: 1
state_dir: /var/lib/lxcer
inventory:
  - name: local
    instances:
      example:
        paths: [/srv/example-data]
        hooks:
          pre:
            - argv: [/usr/local/sbin/lxcer-dump]
          quiesce:
            - argv: [/usr/local/sbin/lxcer-quiesce]
          resume:
            - argv: [/usr/local/sbin/lxcer-resume]
destinations:
  - name: offsite
    path: sftp:backup-repository:/backups/lxcer
    password_file: /etc/lxcer/restic-password
```

Configure `backup-repository` as an SSH alias under the backup account and initialize the Restic repository separately. To schedule this source from a controller, use an SSH agent inventory entry with an absolute `remote_config` pointing to the source's configuration. [Host and instance policies](CONFIGURATION.md#host-and-instance-policies)

Recover the root disk to an isolated instance and restore the bind data to a separate filesystem target. Review the instance's disk mappings and verify application data before reconnecting it. The root-only recovery check omits external mounts. [Recovery guide](RESTORE.md)
