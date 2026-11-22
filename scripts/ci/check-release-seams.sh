#!/usr/bin/env bash
# check-release-seams.sh: a release build of wy contains none of the test seams.
#
# Builds wy the way `make build` / scripts/release/build.sh do (CGO_ENABLED=0, -trimpath,
# -ldflags '-s -w'), then checks that the strings of the test-seam build tags (fakeclock,
# fakeopen, peerstest, trusttest) are absent. A positive control builds with those tags and
# requires the same strings to be present, so the check cannot pass just because the strings
# stopped existing. It also refuses any mention of `trusttest` in the release script and the
# release workflow and in app/scripts/fetch-wy.mjs (the desktop app's wy): the trusted-sender
# test build (peers.md 15) never ships. Both build scripts must clear GOFLAGS.
# It only builds; it never runs wy.
#
#   check-release-seams.sh                 the checks above
#   check-release-seams.sh --scan <path>…  check the wy binaries actually built: every
#                                          wy/wy.exe under <path>, and inside every .tar.gz and
#                                          .zip there (the release workflow runs this on dist/)
set -euo pipefail

GO=${GO:-go}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

# Strings owned by the test-seam build tags (fakeclock: internal/hub/cli/hooks_fakeclock.go;
# fakeopen: internal/termctl/fakeopen.go; peerstest: internal/peers/matrix_peerstest.go;
# trusttest: internal/peers/trust_relax_trusttest.go and internal/version/build_trusttest.go).
markers=(WY_FAKE_CLOCK WY_TEST_WEBPUSH_DIAL WY_TEST_OPENER WY_PEERS_TEST_ALLOW
  "TEST BUILD: approval guard is off" "+trusttest")

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

has() { grep -aqF -- "$2" "$1"; }

# --scan: the built binaries themselves.
if [ "${1:-}" = "--scan" ]; then
  shift
  [ $# -gt 0 ] || { echo "release-check: --scan needs a path" >&2; exit 2; }
  n=0
  bad=0
  scan() {
    n=$((n + 1))
    for m in "${markers[@]}"; do
      if has "$1" "$m"; then
        echo "release-check: FAIL: $1 contains '$m'" >&2
        bad=1
      fi
    done
  }
  for p in "$@"; do
    while IFS= read -r -d '' a; do
      d=$(mktemp -d "$tmp/x.XXXXXX")
      case $a in
      *.tar.gz) tar -xzf "$a" -C "$d" ;;
      *.zip) python3 -I -m zipfile -e "$a" "$d" ;;
      esac
      while IFS= read -r -d '' b; do scan "$b"; done < <(find "$d" -type f \( -name wy -o -name wy.exe \) -print0)
    done < <(find "$p" -type f \( -name '*.tar.gz' -o -name '*.zip' \) -print0)
    while IFS= read -r -d '' b; do scan "$b"; done < <(find "$p" -type f \( -name wy -o -name wy.exe \) -print0)
  done
  [ "$n" -gt 0 ] || { echo "release-check: FAIL: no wy binary found under $*" >&2; exit 1; }
  [ "$bad" -eq 0 ] || exit 1
  echo "release-check: ok: $n built wy binaries hold no test seams"
  exit 0
fi

# 1. The release script, the desktop app's wy build and the Makefile's build must not pass
# build tags; the two build scripts must clear GOFLAGS and may only clear it.
if grep -nE -- "(^|[^a-z])-tags|'-tags'" scripts/release/build.sh app/scripts/fetch-wy.mjs; then
  echo "release-check: FAIL: a release build script passes build tags" >&2
  exit 1
fi
if grep -nE -- 'GOFLAGS=[^[:space:]]' scripts/release/build.sh || ! grep -qF -- 'CGO_ENABLED=0 GOFLAGS= GOOS=' scripts/release/build.sh; then
  echo "release-check: FAIL: scripts/release/build.sh must build with GOFLAGS cleared, and set it to nothing else" >&2
  exit 1
fi
if grep -nE -- "GOFLAGS: *[^' ]|GOFLAGS: *'[^']" app/scripts/fetch-wy.mjs || ! grep -qF -- "GOFLAGS: ''" app/scripts/fetch-wy.mjs; then
  echo "release-check: FAIL: app/scripts/fetch-wy.mjs must build with GOFLAGS cleared, and set it to nothing else" >&2
  exit 1
fi
if grep -nE -- 'build .*-tags' Makefile | grep -v '^[0-9]*:#'; then
  echo "release-check: FAIL: a Makefile build passes build tags" >&2
  exit 1
fi
# The trusted-sender test build turns off the approval guard: never in a release.
if grep -n -- 'trusttest' scripts/release/build.sh .github/workflows/release.yml app/scripts/fetch-wy.mjs; then
  echo "release-check: FAIL: the release build mentions the trusttest tag" >&2
  exit 1
fi

build() { # build <out> [tags]
  local out=$1 tags=${2:-}
  if [ -n "$tags" ]; then
    CGO_ENABLED=0 "$GO" build -trimpath -tags "$tags" -ldflags '-s -w' -o "$out" ./cmd/wy
  else
    CGO_ENABLED=0 "$GO" build -trimpath -ldflags '-s -w' -o "$out" ./cmd/wy
  fi
}

echo "release-check: building the test-seam binary (positive control)"
build "$tmp/wy-seams" fakeclock,fakeopen,peerstest,trusttest
for m in "${markers[@]}"; do
  if ! has "$tmp/wy-seams" "$m"; then
    echo "release-check: FAIL: the control build lacks '$m'; the check would prove nothing" >&2
    exit 1
  fi
done

echo "release-check: building the release binary"
build "$tmp/wy-release"
bad=0
for m in "${markers[@]}"; do
  if has "$tmp/wy-release" "$m"; then
    echo "release-check: FAIL: the release binary contains '$m'" >&2
    bad=1
  fi
done
[ "$bad" -eq 0 ] || exit 1
echo "release-check: ok: no fakeclock, fakeopen, peerstest or trusttest seams in a release build"
