# Storage-native incrementals and reconstruction

`storage-incremental` is a separate strategy. Existing `incremental` keeps its previous mirror-refresh/full-export behavior; unversioned configurations still use snapshot/image/zstd.

## Capture and recoverable chains

On a configured source-side agent, Btrfs uses a read-only subvolume snapshot and `btrfs send -p` after the first full stream. ZFS uses a dataset snapshot and `zfs send -i`. No full LXD export or container relay is performed for this strategy. Each stream is staged, checksummed and backed up with Restic. The manifest records full/incremental mode, base job, ordered parents and exact snapshot IDs per repository. New source checkpoints become current only after required verification and catalog receipts succeed. An interrupted send/upload does not advance the checkpoint.

Each new job verifies required repository ancestors before producing a dependent stream. Optional destinations are not marked verified when their ancestors are absent. Cleanup-only failures can retain a valid checkpoint if every destination has verified data and receipts. Only the latest committed source snapshot is required for the next send; older source snapshots are deleted by exact job identity after verification. Failed source snapshots remain retained.

`max_chain` defaults to 8 members and rotates to a new full stream. A missing chain is an error by default. `incremental_fallback: full` explicitly permits a full portable/image fallback on unsupported drivers, or a fresh standalone native stream when repository parents are missing. Fallback reasons are recorded. It never reports a full stream as a delta.

Retention operates on whole chain roots, retains at least the newest completed chain, and protects chains with unfinished/unreceipted data. It cannot remove a base while keeping its child. Source host and instance scope are mandatory; prune and forced unlock remain separate administrative operations. Receipts remain as catalog history. Retention is conservative and can keep more data than the nominal count.

## Configuration

See [storage-incremental.yml](../examples/storage-incremental.yml). Identify actual storage paths through your disposable-host qualification; do not assume that these example paths describe your LXD installation.

- Btrfs `storage_source` must be the instance subvolume. `native_root` may be its `rootfs` directory. `snapshot_dir` must be outside that source, on the same filesystem. State and snapshot directories cannot be captured recursively.
- ZFS `storage_source` is the filesystem dataset. Mounted datasets with an absolute mountpoint work directly. For LXD's `mountpoint=legacy`, the adapter uses `findmnt` JSON to locate the dataset root. Unmounted stopped datasets require a deliberately mounted source or an explicit portable fallback; lxcer does not mount production datasets automatically.
- Child ZFS datasets and nested Btrfs subvolumes require separate policies. They are rejected rather than omitted. Root-only streams do not include external bind mounts or custom LXD volumes.
- LVM, directory and Ceph drivers do not use the Btrfs/ZFS stream adapter. Choose an explicit full fallback or the existing portable strategy. Restic deduplication of a full archive is not a source incremental.
- Source-side execution requires the relevant storage administration privileges. Use an explicit `agent_binary` path, such as `/opt/lxcer/lxcer`, to coexist with `/usr/local/bin/lxcer`. SSH controllers pass a stable inventory host ID, preventing unrelated agents from sharing `host=local` retention scope. Legacy local invocations keep their existing identity.

Application hooks quiesce writers only when explicitly configured. For snapshots/streams without additional live filesystem paths, resume occurs after the snapshot is created, before publication/send/upload. Additional direct paths keep quiescence until those paths are captured. A running database still needs an application-specific recovery procedure.

## Automated reconstruction

Modern `restore` and `restore-test` reconstruct native Restic roots into a unified LXD image, import/init under the requested name, map devices/configuration, validate ownership and keep the instance stopped. `-native-target` retains the existing raw filesystem restore behavior.

For streams, restore verifies every repository member and checksum, then replays full and delta data in order. Btrfs receive uses private staging on a Btrfs filesystem; configure `state_dir` accordingly. ZFS receive requires `-receive-dataset <disposable-parent>` and creates a random child dataset with an ownership property, disabled automount and a private mountpoint. It never uses forced receive/rollback. Journals identify receiver directories/parents before work begins. Successful cleanup deletes only these owned receivers; failures retain evidence for review.

Image creation uses GNU tar to preserve sparse files, hardlinks, symlink targets, permissions, ACLs and xattrs. Captured LXD ID maps translate numeric owners; named ACL and namespaced capability IDs are normalized only in restored private staging. Required metadata privileges are enforced by system calls; insufficient privileges fail instead of silently dropping metadata. Unmapped owners, missing architecture/ID-map metadata, virtual-machine filesystem reconstruction and escaping symlinks fail safely. A root without sufficient metadata can still be restored offline using `-native-target`.

A test restore requires a separate explicitly isolated host and a root-only profile. It validates devices before optionally executing an application health command. It does not prove physical network isolation; provision the test host outside production networks. Full archives on older servers without name override can import under a free original name and then rename, with both names locked. If that original name already exists, restoration fails without overwriting it.

## Space and measurement

`max_staging_bytes` defaults to 32 GiB for loaded configs. The monitor checks all job/restore staging under `state_dir`, includes retained failures, checks free space during work and cancels on limits. Stream output also has a bounded writer. Offline restore targets receive their own limit checks. Limits are configurable for large backups; strict physical byte enforcement requires filesystem/project quotas because an external exporter can overshoot between monitor samples. LXD source-pool capacity is queried where available; `require_pool_space: true` rejects unknown capacity. Otherwise the manifest explicitly records that capacity was NOT VERIFIED. Headroom checks are not a guarantee that a full publication will fit: size source pools for publication, snapshots and application growth.

Per-job measurements include archive/stream bytes, capture duration, staging peak, parent count and Restic `data_added_packed` per destination. Packed data bytes exclude transport protocol overhead. Driver-reported referenced/exclusive snapshot bytes are recorded when available; `-1` means unavailable. Btrfs extent accounting can add metadata scanning time. The receiver/import path necessarily materializes a root/image during recovery; incremental capture does not make recovery free of full-data work.

The command-model tests deliberately generate a 32 KiB base and 1 KiB delta; those are simulation sizes, not filesystem performance claims. Real timing/space/transfer measurements require the guarded kernel harness below. No real Btrfs/ZFS delta reduction was verified in the development environment.

## Disposable protocol harness

```sh
LXCER_TEST_DISPOSABLE=YES \
LXCER_TEST_STORAGE_DRIVER=btrfs \
LXCER_TEST_STREAM_PARENT=/mnt/btrfs/lxcer-test-streams \
go test -tags=storageintegration -run '^TestRealStorageStreamRoundTrip$' -v ./...
```

For ZFS use a disposable parent dataset such as `tank/lxcer-test-streams`. The harness creates only a random child/subvolume, captures 8 MiB of disposable random data, changes 4 KiB in place, receives the chain through Restic and verifies its recovered image payload. It reports stream sizes, packed upload, capture and restore duration. LXD import is modeled in this protocol harness; separately run the real LXD matrix to qualify the daemon/storage combination. Unconfigured runs skip as NOT VERIFIED; explicitly enabled runs with invalid prerequisites fail. Manual CI workflows require the protected disposable environment/runner. Reset that environment after reviewing retained failure resources.

References: [Btrfs send](https://btrfs.readthedocs.io/en/latest/btrfs-send.html), [Btrfs receive](https://btrfs.readthedocs.io/en/latest/btrfs-receive.html), [ZFS send](https://openzfs.github.io/openzfs-docs/man/master/8/zfs-send.8.html), [ZFS receive](https://openzfs.github.io/openzfs-docs/man/master/8/zfs-receive.8.html), [LXD image format](https://canonical.com/lxd/docs/latest/reference/image_format/).
