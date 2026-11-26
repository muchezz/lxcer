# v0.4.0-rc.1 compatibility

**Verified** means the listed command or component actually ran. **Expected** means source, package or runtime requirements agree and the target still needs qualification. **Unsupported** means a required capability is absent or the implementation rejects that combination.

## Verified in qualification

- Ubuntu 24.04 AMD64 ran the lxcer CLI, local Restic workflows and a disposable Node Exporter scraper. The Go binaries for both architectures were statically linked. The clean-build AMD64 binary ran natively, and the ARM64 binary ran `version` and `validate` under QEMU 8.2 user-mode emulation. Both reported the same source commit and build date.
- The LXD snap's installed 5.21.9 CLI launcher ran locally. The shipped snap wrapper and an Ubuntu 18.04 LXD 3.0.3 `lxd-client` package extracted on Ubuntu 24.04 both ran the capability command against disposable Unix and HTTPS endpoints. The endpoints returned the corresponding upstream extension lists. This exercised client packaging, request routing and negotiation. Full daemon backups and restores still require qualification.
- Restic 0.16.0 passed backup summary parsing, exact snapshot and tree verification, `check --read-data`, legacy archive adoption, and catalog recovery using a disposable local repository. Local workflows also passed with Ubuntu 24.04's Restic 0.16.4.
- Zstandard 1.3.8 and 1.5.5 passed local compression and decompression checks. The 1.3.8 check used the same `-T1 --rsyncable` arguments as lxcer and verified the restored bytes.
- Node Exporter 1.7.0 scraped a disposable lxcer textfile successfully. The alert file passed `promtool check rules` in 2.1.0, 2.15.2, 2.31.2, 2.45.3 and 2.53.5. `promtool check metrics` passed with 2.45.3.

### Mixed LXD capability results

| LXC client used on Ubuntu 24.04 | LXD API served by the disposable endpoint | Export | Refresh / instance API |
|---|---|---|---|
| Snap 5.21.9 | 3.0.3 over local Unix and remote HTTPS | Disabled | Both disabled |
| Snap 5.21.9 | 5.21.9 over remote HTTPS | Enabled | Both enabled |
| Ubuntu package 3.0.3 | 3.0.3 over local Unix and remote HTTPS | Disabled | Both disabled |
| Ubuntu package 3.0.3 | 5.21.9 over remote HTTPS | Disabled | Refresh disabled. Instance API advertised by target. |

The same invocation probed the local and remote aliases separately. The client name-argument result describes the invoked client. Restore also requires the target server's `backup_override_name` extension to pass a replacement name directly. Direct backup selection needs both the server's `container_backup` extension and the client's export command. The refresh strategy needs `container_incremental_copy` and the client's `--refresh` flag. A mismatched client can therefore disable a feature even when the server advertises it. [LXD API extension reference](https://canonical.com/lxd/docs/latest/api-extensions/)

## Expected by source and package review

The release targets Linux `GOAMD64=v1` and `GOARM64=v8.0` with `CGO_ENABLED=0`. lxcer itself has no glibc dependency. LXD, Restic, Zstandard, SSH and GNU tar are separate executables with their own requirements. Go 1.26 lists Linux kernel 3.2 or later, which is below the stock kernel for each Ubuntu release in this table. [Go minimum requirements](https://go.dev/wiki/MinimumRequirements) · [Ubuntu release cycle](https://ubuntu.com/about/release-cycle)

| Ubuntu LTS | Archive Restic | Archive Zstandard | Host qualification |
|---|---|---|---|
| 18.04 Bionic | 0.8.3 | 1.3.3 | Expected lxcer binary portability. Supply Restic ≥ 0.16.0 and, for compressed backups, Zstandard ≥ 1.3.8 separately. The packaged LXC 3.0.3 client was exercised on 24.04. |
| 20.04 Focal | 0.9.6 | 1.4.4 | Expected. Supply Restic ≥ 0.16.0 separately. |
| 22.04 Jammy | 0.12.1 | 1.4.8 | Expected. Supply Restic ≥ 0.16.0 separately. |
| 24.04 Noble | 0.16.4 | 1.5.5 | Verified AMD64 CLI and local repository operation. Live LXD backup and restore still need qualification. |
| 26.04 Resolute | 0.18.1 | 1.5.7 | Expected. The archive versions meet both dependency floors. |

Values show the Ubuntu archive's base upstream versions. Use the security-patched package or a verified upstream binary compatible with the host. [Ubuntu Restic packages](https://launchpad.net/ubuntu/+source/restic) · [Ubuntu Zstandard packages](https://launchpad.net/ubuntu/+source/libzstd) · [Restic 0.16.0](https://github.com/restic/restic/releases/tag/v0.16.0) · [Zstandard 1.3.8](https://github.com/facebook/zstd/releases/tag/v1.3.8)

| LXD server family | Expected path, subject to the actual client and advertised extensions |
|---|---|
| 3.x | 3.0.3 uses snapshot, image publication and image export. Later 3.x such as 3.23 already advertise portable export and refresh. |
| 4.x | Portable export for capable clients. The 4.0 client/server sources reviewed use the archive's original name on import. Recovery needs that name free before renaming. |
| 5.0 | Portable export, import name override and custom volume backup when the corresponding capabilities are available. |
| 5.21 | Same expected features as 5.0. Client negotiation at 5.21.9 was exercised as described above. |
| Newer | 6.10.1 and current upstream sources still advertise the extensions lxcer uses. Qualify newer servers and archive formats independently. |

[LXD 3.0.3 source](https://github.com/canonical/lxd/tree/lxd-3.0.3) · [LXD 3.23 extensions](https://github.com/canonical/lxd/blob/lxd-3.23/shared/version/api.go) · [LXD 4.0.9 import source](https://github.com/canonical/lxd/blob/lxd-4.0.9/lxc/import.go) · [LXD 5.0.5 extensions](https://github.com/canonical/lxd/blob/lxd-5.0.5/shared/version/api.go) · [LXD 5.21.9 extensions](https://github.com/canonical/lxd/blob/lxd-5.21.9/shared/version/api.go) · [LXD 6.10.1 extensions](https://github.com/canonical/lxd/blob/lxd-6.10.1/shared/version/api.go)

lxcer uses the `lxc` found in the backup account's PATH. The example systemd unit includes `/snap/bin` as well as package binary directories. For a snap client, configure remotes and trust from the backup account using the snap's own config directory. A native package client uses its own config and socket defaults. If both clients are installed, verify which `lxc` wins on the service PATH. `capabilities -local` and `capabilities -remote-host NAME` report the actual client and server. [LXD installation options](https://canonical.com/lxd/docs/default/installing/) · [LXD client configuration](https://canonical.com/lxd/docs/v5.21/faq/)

## Unsupported combinations

- Restic older than **0.16.0**. lxcer requires the repository lock retry option, JSON snapshot and tree output, and verified restores.
- Zstandard older than **1.3.8** for lxcer compression. The earlier Bionic implementation lacks `--rsyncable`. Uncompressed operations do not require the zstd CLI. Recovery of a zstd archive still needs a compatible decoder.
- Portable export or import overrides when the required client command or server extension is absent. The snapshot/image path supports containers only and does not preserve the original instance snapshot history.
- Guaranteed newer-to-older LXD archive imports, VM filesystem-stream reconstruction, non-Linux hosts and architectures other than AMD64 and ARM64.

## Live qualification remaining

Provision disposable hosts with the intended Ubuntu kernel, LXD package and storage driver. As the scheduled account, run capability discovery on each source and target. Back up a running and a stopped instance locally and remotely, verify receipts, restore to new names and compare known files and application data. Repeat with the intended Restic backend, hooks and any storage stream strategy. Include a native ARM64 run before claiming native ARM64 qualification.
