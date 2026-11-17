# Restore and disaster recovery

Original `-a restore` invocations with an unversioned config still select `restore_restic_repo`, restore `<container>.tar.zst`, and start the instance as before. They now verify the selected repository snapshot/path, refuse collisions and retain failed staging. `-stopped` suppresses startup. Multi-entry `-restore-list` and bounded `-concurrently` operation remain available. No new manifest or adoption step is required for this original workflow.

A repository upload is not a tested recovery. Periodically perform a disposable restore test, inspect filesystem content and run application-specific checks. Record measured backup age and restore duration against your RPO/RTO. Keep source configuration, network definitions, profile definitions, certificates and external storage recovery procedures in a separate administrative backup.

## Discover and plan

```sh
./lxcer discover -config conf.yml -destination primary
./lxcer list -config conf.yml
./lxcer restore-plan -config conf.yml -manifest /state/lxcer-ID/manifest.json \
  -destination primary -remote-host lab -as app-dr -storage-pool recovery
```

Plans query target capabilities and collisions without importing instances. No overwrite mode exists. Rename or remove a preexisting instance separately through an explicitly authorized administrative workflow. `-container app -source-host source` selects the newest verified catalog entry; ambiguous source names require a source host. `-snapshot` pins an exact data snapshot ID. A restore list (`source:target` per line) selects the appropriate manifest for every source and aggregates failures; malformed or duplicate source lines fail.

```sh
./lxcer restore -config conf.yml -container app -source-host source \
  -destination primary -remote-host recovery -as app-dr \
  -storage-pool recovery -network-map prod=dr
```

The archive is restored into private staging with Restic `--verify`, its SHA256 is compared to the manifest, and only then is it imported. Full imports use capability-gated name and pool overrides. Legacy image imports use `init`, never `launch`. Instance configuration is reconstructed from the sidecar, dropping volatile/raw host-specific keys, applying explicit devices instead of source profiles, mapping networks/pool and disabling autostart. Post-validation requires a stopped instance and a matching ownership marker. Review privileged security settings for normal restores before starting manually.

Managed volume archives require `-restore-volumes`. New names avoid overwriting old volumes, volume checksums are verified and instance disk sources are remapped. Existing target names fail. On partial failure, imported volumes and unmarked imported instances are retained, not guessed safe to delete. A private `restore-*/restore.json` journal records the target/source job. Inspect it and target ownership before manual cleanup. Normal restores with excluded/bind-mounted dependencies require separately restoring/reconnecting those dependencies first; the current CLI rejects automatic activation with those attachments. A root-only isolated test remains possible.

Native filesystem restores require a new `-native-target /private/new-directory`; paths are recreated beneath that directory, never over the original host paths. Restic preserves supported Unix ownership, permissions and extended attributes subject to caller privileges and filesystem support. Reconstruct/import the LXD instance separately using manifest configuration and a qualified filesystem migration procedure. Native mode does not invent an instance import format. An offline native restore test can verify files, but cannot execute LXD application health commands.

## Isolated restore verification

Prepare a separate LXD test host outside production networking and a profile containing only a root disk. A separate remote name is an operator assertion, not proof of physical network isolation. Do not alias the production server under a test name.

```sh
./lxcer restore-test -config conf.yml -manifest /state/lxcer-ID/manifest.json \
  -destination primary -remote-host isolated-lab -as disposable-test \
  -isolated-target -profile root-only -storage-pool scratch \
  -health-argv '["/usr/local/sbin/check-recovery"]'
```

The command rejects the source/local target and unsafe profiles, removes external/network devices and source security/raw overrides, validates the stopped instance's expanded devices, and starts it only when a health command is explicitly supplied. A root-only container has no normal network interface; network services may need adapted offline health checks. The health command runs as argv inside that isolated instance. Test-owned instances are deleted with a bounded cleanup context even after health-check failure. Imported instances whose ownership could not be established are retained with a journal. Test results persist in the source manifest and feed metrics. Application-specific restore steps, such as importing database dumps, belong in your audited health/recovery command.

## Lost catalog

Use Restic to list `lxcer-receipt` snapshots and select the exact ID:

```sh
# Use your existing secure Restic environment.
restic snapshots --json --tag lxcer-receipt
./lxcer recover-catalog -config conf.yml -destination primary \
  -snapshot RECEIPT_SNAPSHOT_ID -target /private/new-catalog-recovery
```

The recovered manifest includes data snapshot IDs and original archive paths. Pass its actual recovered pathname to `-manifest`. Export-time sidecars in `lxcer-data` snapshots remain useful if receipt upload was interrupted. They lack final verification receipts; select and verify an exact data snapshot manually. Receipt snapshots are retained as catalog history even when scoped data retention expires an old archive; a historical receipt alone does not prove the data snapshot still exists.

For modern commands that need explicit metadata, pool/network mapping or catalog integration with old `.tar.zst` archives, use `discover -legacy`, obtain a JSON instance object (for example a saved object from `lxc list --format json`), then adopt it:

```sh
./lxcer adopt-legacy -config conf.yml -destination primary -source-host old \
  -snapshot EXACT_ID -artifact-path /original/working/directory/app.tar.zst \
  -legacy-metadata /private/instance.json
```

Adoption restores/verifies the old archive, records its checksum and supplied metadata, and creates a local catalog entry. It does not claim missing external data or historical application consistency. Missing original metadata must be reconstructed and reviewed explicitly. Adopted jobs never authorize artifact cleanup.

## Interruption and recovery

`cleanup` and `recover` default to preview. `recover -execute` acquires instance locks, runs persisted best-effort resume hooks for interrupted quiescence, marks unfinished jobs failed, and retries cleanup only for Verified jobs with verified required receipts. A SIGTERM cancels work but allows bounded resume handling. SIGKILL/power loss requires running recovery before scheduling another backup; no program can execute resume hooks while its host is down. Failed staging remains available; never delete it solely because an upload command exited zero.
