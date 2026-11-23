#!/bin/sh
# Cross-builds the six release archives of docs/spec/setup.md 1.1 into dist/release/,
# writes SHA256SUMS and copies the install scripts next to them. Plain shell + go build;
# the CI release job and the E2E harness run this same script.
#
#   scripts/release/build.sh <version>        version with or without a leading v
#
# Environment:
#   WY_RELEASE_DIR      output folder (default <repo>/dist/release)
#   WY_RELEASE_TARGETS  space-separated os_arch list to build instead of all six
#                       (used by the E2E harness, e.g. "linux_amd64")
#   GO                  go command (default go)
set -eu

die() { echo "build.sh: $*" >&2; exit 1; }

[ $# -eq 1 ] || die "usage: build.sh <version>"
ver=${1#v}
case $ver in
	''|*[!0-9A-Za-z.+-]*) die "bad version: $1" ;;
esac

root=$(cd "$(dirname "$0")/../.." && pwd)
out=${WY_RELEASE_DIR:-$root/dist/release}
GO=${GO:-go}
targets=${WY_RELEASE_TARGETS:-"linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64 windows_arm64"}

commit=$(cd "$root" && git rev-parse --short HEAD 2>/dev/null || echo unknown)
pkg=github.com/mraj602/watchyard/internal/version
ldflags="-s -w -X $pkg.Version=$ver -X $pkg.Commit=$commit"

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
	else openssl dgst -sha256 "$1" | awk '{print $NF}'
	fi
}

# pack <archive> <stage> <files...>: tar.gz or zip of files in stage.
pack() {
	name=$1 stage=$2
	shift 2
	case $name in
	*.zip)
		if command -v zip >/dev/null 2>&1; then
			(cd "$stage" && zip -q -X "$out/$name" "$@")
		else
			python3 -I - "$out/$name" "$stage" "$@" <<'PY'
import sys, zipfile, os
dst, stage, *files = sys.argv[1:]
with zipfile.ZipFile(dst, "w", zipfile.ZIP_DEFLATED) as z:
    for f in files:
        z.write(os.path.join(stage, f), f)
PY
		fi ;;
	*) (cd "$stage" && tar -czf "$out/$name" "$@") ;;
	esac
}

# No build flag comes in from outside (peers.md 15: a test build never ships): GOFLAGS is
# cleared for every build below, and one set with `go env -w`, which an empty variable does
# not override, stops the release.
goflags_cfg=$(cd "$root" && GOFLAGS= $GO env GOFLAGS)
[ -z "$goflags_cfg" ] || die "go env sets GOFLAGS to $goflags_cfg; a release build takes no extra flags (go env -u GOFLAGS)"

rm -rf "$out"
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

for t in $targets; do
	os=${t%_*}
	arch=${t#*_}
	bin=wy
	ext=tar.gz
	if [ "$os" = windows ]; then bin=wy.exe; ext=zip; fi
	stage=$work/$t
	mkdir -p "$stage"
	echo "build $t"
	(cd "$root" && CGO_ENABLED=0 GOFLAGS= GOOS=$os GOARCH=$arch \
		$GO build -trimpath -ldflags "$ldflags" -o "$stage/$bin" ./cmd/wy)
	set -- "$bin"
	for f in LICENSE README.md; do
		if [ -f "$root/$f" ]; then cp "$root/$f" "$stage/$f"; set -- "$@" "$f"
		else echo "warning: $f missing from the repo, not added to the $t archive" >&2
		fi
	done
	pack "wy_${ver}_$t.$ext" "$stage" "$@"
done

: > "$out/SHA256SUMS"
for f in "$out"/*_"$ver"_*; do
	printf '%s  %s\n' "$(sha256_of "$f")" "$(basename "$f")" >> "$out/SHA256SUMS"
done

cp "$root/install/install.sh" "$root/install/install.ps1" "$out/"
echo "release $ver: $(find "$out" -type f | wc -l | tr -d ' ') files in $out"
