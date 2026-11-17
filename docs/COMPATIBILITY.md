# Compatibility evidence

Capability discovery reads `GET /1.0`, including `environment.server_version` and `api_extensions`, and separately probes client help. It does not infer extensions solely from a version string. Server/client mismatch disables unsupported export/refresh strategies. Instance terminology is used only with the `instances` extension; legacy cleanup uses the containers API.

| Server target | Automated evidence | Intended path | Real daemon qualification |
|---|---|---|---|
| 3.0.3 / 3.0.x | Wrapped response, no modern export/refresh, running/stopped command fixtures | Snapshot -> image -> export + metadata | Unverified |
| 4.0 LTS | Legacy and modern capability fixtures | Capability-selected portable export or image | Unverified |
| 5.0 LTS | Export/refresh and legacy client fixtures | Portable export; refresh if advertised | Unverified |
| 5.21 / modern 5.x | Capability and mismatch fixtures | Portable export, managed volumes when supported | Unverified |
| 6.x | Capability, restore and storage simulations | Portable export; rename/pool mapping gated by extensions | Unverified |
| Future version string | Same capabilities, both client variants | Negotiate known features, fail unknown operation safely | Unverified |

Fixtures test adapter decisions and argv contracts; they do not establish that a particular release exports the modeled extensions. No row is production-certified by these tests. Old clients may not support JSON or remote query/image/volume commands required here; discovery and commands fail rather than claiming success. Full import name override requires both server capability and client help support. Otherwise the adapter can import under a free original name and rename on the same host; collisions fail before mutation. Pool mapping requires `container_backup_override_pool`. Portable archive formats still evolve; exporting from a newer server does not guarantee import into an older server. Use the snapshot/image adapter for a qualified legacy recovery path and retain full instance configuration sidecars.

| Driver | Implemented handling | Incremental transfer behavior | Real qualification |
|---|---|---|---|
| Btrfs | Detected; portable exports and storage snapshots delegated to LXD | LXD refresh may use native snapshot transfer | Unverified |
| ZFS | Detected; portable exports; avoid dataset-specific archive requirement | LXD refresh may use native snapshot transfer | Unverified |
| LVM | Detected; portable export, sufficient thin-pool/export space required | LXD chooses supported refresh/migration path | Unverified |
| Directory (`dir`) | Detected; portable export, copies may be expensive | Refresh can require substantial filesystem scanning | Unverified |
| Ceph (`ceph`) | Detected; use server-mediated export; external cluster resources remain separate | Depends on daemon/cluster migration support | Unverified |
| Legacy no root pool | Recorded as `legacy-unknown` | No inferred storage-native behavior | Unverified |

These adapters intentionally do not operate directly on production ZFS datasets, LVM devices or Ceph objects. Native requires explicitly available filesystem paths. Storage-native Btrfs/ZFS send/receive and ID-map-aware image reconstruction are implemented but real kernel/daemon interoperability remains NOT VERIFIED; see STORAGE_STREAMS.md. Custom volume backup depends on server extension plus working client command; block volumes and VM split-image publication are not supported by the legacy strategy. Root disk export never implies custom disks, bind mounts or the underlying Ceph cluster are included.

## Disposable-host matrix procedure

Provision separate VMs with exact LXD versions and driver pools; configure an explicit root-only profile, trusted test image and a remote called `lxcer-test-VERSION-DRIVER`. Keep that infrastructure away from production credentials and networks. Then run:

```sh
LXCER_TEST_DISPOSABLE=YES \
LXCER_TEST_REMOTE=lxcer-test-6-dir \
LXCER_TEST_PROFILE=root-only \
LXCER_TEST_IMAGE=images:alpine/3.20 \
LXCER_TEST_POOL=default \
go test -tags=lxdintegration -run '^TestIsolatedLXD$' -v -timeout 40m ./...
```

The harness creates uniquely named stopped and running instances, writes a sentinel in the running instance, backs up into a temporary local Restic repository, restores stopped instances and checks the sentinel. Cleanup checks job ownership markers. The default run skips with no explicit disposable configuration. The manual self-hosted CI workflow requires an isolated runner label/environment and never schedules on production hosts. Repeat for all version/driver combinations and attach logs before promoting a row to verified. Image availability and old server compatibility must be established by the test operator.

References: [LXD API extensions](https://canonical.com/lxd/docs/latest/api-extensions/), [portable backups](https://canonical.com/lxd/docs/latest/howto/instances_backup/), [storage driver capabilities](https://canonical.com/lxd/docs/latest/reference/storage_drivers/), [Restic scripting](https://restic.readthedocs.io/en/stable/075_scripting.html).

## Expanded executable matrix

`TestBackupRestoreCompatibilityMatrix` runs running/stopped backups and restores for 3.0.3, 4.0, 5.0 and 6.0 command models across Btrfs/ZFS/LVM/dir/Ceph. It asserts that 3.0.3 never executes `lxc export`, and exercises older import-then-rename behavior. Split image exports are independently verified. These are **PASS adapter simulations**, not daemon qualification.

The manual LXD workflow accepts expected version/driver values and checks them against the actual server. The guarded `TestLegacy303BtrfsSFTP` scenario requires two disposable `lxcer-test-*` hosts, source version 3.0.3, Btrfs and a dedicated SFTP repository. It creates a random running application simulator with a default 256 MiB payload/heartbeat and a stopped instance, verifies both backups before cleanup, restores on the other host with payload checksum checks, and asserts nonzero CLI failure when repository tree verification is injected to fail. The scenario mutates only instances it creates on disposable hosts. Repository initialization requires a new path containing `/lxcer-test-`; retained failure evidence is intentional. Actual application/database consistency requires separate application qualification.

Set `LXCER_TEST_DISPOSABLE=YES`, source/restore remotes and root-only profiles, image, pool, dedicated SFTP repository and password environment to run this opt-in test. With no opt-in it skips as NOT VERIFIED; with opt-in and invalid prerequisites it fails. The default development run supplied none of that infrastructure. **Every real LXD version/backend and the SFTP scenario remain NOT VERIFIED.**
