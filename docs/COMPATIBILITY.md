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

Fixtures test adapter decisions and argv contracts; they do not establish that a particular release exports the modeled extensions. No row is production-certified by these tests. Old clients may not support JSON or remote query/image/volume commands required here; discovery and commands fail rather than claiming success. Full import rename requires `backup_override_name`; original-name imports can use older servers. Pool mapping requires `container_backup_override_pool`. Portable archive formats still evolve; exporting from a newer server does not guarantee import into an older server. Use the snapshot/image adapter for a qualified legacy recovery path and retain full instance configuration sidecars.

| Driver | Implemented handling | Incremental transfer behavior | Real qualification |
|---|---|---|---|
| Btrfs | Detected; portable exports and storage snapshots delegated to LXD | LXD refresh may use native snapshot transfer | Unverified |
| ZFS | Detected; portable exports; avoid dataset-specific archive requirement | LXD refresh may use native snapshot transfer | Unverified |
| LVM | Detected; portable export, sufficient thin-pool/export space required | LXD chooses supported refresh/migration path | Unverified |
| Directory (`dir`) | Detected; portable export, copies may be expensive | Refresh can require substantial filesystem scanning | Unverified |
| Ceph (`ceph`) | Detected; use server-mediated export; external cluster resources remain separate | Depends on daemon/cluster migration support | Unverified |
| Legacy no root pool | Recorded as `legacy-unknown` | No inferred storage-native behavior | Unverified |

These adapters intentionally do not operate directly on production ZFS datasets, LVM devices or Ceph objects. Native requires explicitly available filesystem paths. Custom volume backup depends on server extension plus working client command; block volumes and VM split-image publication are not supported by the legacy strategy. Root disk export never implies custom disks, bind mounts or the underlying Ceph cluster are included.

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
