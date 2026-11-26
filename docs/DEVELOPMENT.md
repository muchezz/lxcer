# Builds and releases

Development is maintained independently at [muchezz/lxcer](https://github.com/muchezz/lxcer). The Go module retains the original `github.com/pavel1337/lxcer` path. The current release candidate is `v0.4.0-rc.1`. See [project attribution](../README.md#attribution).

## Build from source

Use Go 1.26 or later. Run these commands from the repository root:

```sh
go mod tidy
go fmt ./...
go vet ./...
go build -trimpath -o ./lxcer .
./lxcer version
./lxcer validate -config conf.yml
```

## Build the release candidate

From a clean checkout of the intended source commit:

```sh
test -z "$(git status --porcelain)" &&
  ./scripts/build-release.sh v0.4.0-rc.1 dist/v0.4.0-rc.1 &&
  (cd dist/v0.4.0-rc.1 && sha256sum -c SHA256SUMS)
```

The script uses the toolchain pinned in `go.mod` and builds static Linux AMD64 and ARM64 binaries. Each binary embeds the requested version, source commit, and commit date. A local build from a modified checkout appends `-dirty` to the commit metadata. The output includes the checksums, changelog, [project attribution](../NOTICE), and Go and YAML license notices. The GitHub workflow requires a clean checkout and uploads workflow artifacts only when redistribution has been approved and `LXCER_REDISTRIBUTION_APPROVED=true`.

## Upgrade and roll back

Verify the bundle checksums, then install the matching binary alongside the existing one. For AMD64:

```sh
cd dist/v0.4.0-rc.1
sha256sum -c SHA256SUMS
install -Dm755 lxcer-linux-amd64 "$HOME/.local/lib/lxcer/v0.4.0-rc.1/lxcer"
```

Use `lxcer-linux-arm64` on ARM64. Pause scheduled backups and let in-flight jobs finish. Preserve the existing binary, configuration, and private state directory. Use a [disposable LXD host](COMPATIBILITY.md) and Restic repository to qualify backup and restore before switching the scheduled executable. The [systemd service and timer examples](../examples) show the settings you can adapt to the backup account.

For rollback, pause scheduling and use the candidate's `recover` command to inspect unfinished jobs and resolve outstanding resume hooks before switching back. Restore the previous binary and its matching configuration. Keep modern manifests and repository snapshots available for recovery with the candidate or Restic. The original binary can restore its own archive format with the old configuration.

## Redistribution

No project license grant has been identified in the original repository or its published tags. Obtain written permission or a license from the upstream rights holders before distributing source or binaries. Preserve the original authorship, module path, and dependency notices included in the build bundle. [Original lxcer repository](https://github.com/pavel1337/lxcer) · [GitHub licensing guidance](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/licensing-a-repository)
