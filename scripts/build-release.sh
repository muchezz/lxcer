#!/bin/sh
set -eu
release_version=${1:?version required}
case "$release_version" in *[!a-zA-Z0-9._-]*|'') echo 'Invalid version' >&2; exit 2;; esac
release_commit=$(git rev-parse HEAD)
release_date=$(git show -s --format=%cI HEAD)
mkdir -p dist
for release_arch in amd64 arm64; do
 CGO_ENABLED=0 GOOS=linux GOARCH=$release_arch go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$release_version -X main.commit=$release_commit -X main.buildDate=$release_date" -o "dist/lxcer-linux-$release_arch" .
done
(cd dist && sha256sum lxcer-linux-* > SHA256SUMS)
git log --format='- %s (%h)' > dist/CHANGELOG.md
