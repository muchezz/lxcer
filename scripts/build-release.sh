#!/bin/sh
set -eu

release_version=${1:?version required}
release_dir=${2:-dist}
case "$release_version" in *[!a-zA-Z0-9._-]*|'') echo 'Invalid version' >&2; exit 2;; esac
release_commit=$(git rev-parse HEAD)
release_date=$(git show -s --format=%cI HEAD)
release_changes=$(git status --porcelain --untracked-files=normal)
if [ -n "$release_changes" ]; then
    if [ "${GITHUB_ACTIONS:-}" = true ]; then
        echo 'Release workflow requires a clean checkout' >&2
        exit 1
    fi
    release_commit="$release_commit-dirty"
    echo 'Building a local candidate from a modified checkout' >&2
fi
release_toolchain=$(awk '$1 == "toolchain" {print $2}' go.mod)
export GOTOOLCHAIN="${release_toolchain:?go.mod must specify the release toolchain}"
mkdir -p "$release_dir/licenses"
go mod download gopkg.in/yaml.v3
yaml_dir=$(go list -m -f '{{.Dir}}' gopkg.in/yaml.v3)
cp NOTICE "$release_dir/NOTICE"
release_goroot=$(go env GOROOT)
cp "$release_goroot/LICENSE" "$release_dir/licenses/Go-LICENSE"
cp "$release_goroot/PATENTS" "$release_dir/licenses/Go-PATENTS"
cp "$yaml_dir/LICENSE" "$release_dir/licenses/yaml.v3-LICENSE"
cp "$yaml_dir/NOTICE" "$release_dir/licenses/yaml.v3-NOTICE"
cp licenses/Apache-2.0.txt "$release_dir/licenses/"
for release_arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$release_arch GOAMD64=v1 GOARM64=v8.0 go build -trimpath -buildvcs=false \
        -ldflags "-s -w -X main.version=$release_version -X main.commit=$release_commit -X main.buildDate=$release_date" \
        -o "$release_dir/lxcer-linux-$release_arch" .
done
(
    cd "$release_dir"
    sha256sum lxcer-linux-amd64 lxcer-linux-arm64 > SHA256SUMS
    sha256sum -c SHA256SUMS
    if [ "$(uname -m)" = x86_64 ]; then
        expected="lxcer $release_version commit=$release_commit built=$release_date"
        actual=$(./lxcer-linux-amd64 version)
        if [ "$actual" != "$expected" ]; then
            echo 'AMD64 release metadata verification failed' >&2
            exit 1
        fi
        printf '%s\n' "$actual"
    fi
)
git log --format='- %s (%h)' > "$release_dir/CHANGELOG.md"
