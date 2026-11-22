#!/usr/bin/env bash
# check-release-seams.sh: a release build of wy contains none of the test seams.
#
# Builds wy the way `make build` / scripts/release/build.sh do (CGO_ENABLED=0, -trimpath,
# -ldflags '-s -w'), then checks that the strings of the test-seam build tags (fakeclock,
# fakeopen, peerstest, trusttest) are absent. A positive control builds with those tags and
# requires the same strings to be present, so the check cannot pass just because the strings
# stopped existing. It also refuses any mention of `trusttest` in the release script and the
# release workflow: the trusted-sender test build (peers.md 15) never ships.
# It only builds; it never runs wy.
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

# 1. The release script and the Makefile's build must not pass build tags.
if grep -nE -- '(^|[^a-z])-tags|GOFLAGS' scripts/release/build.sh; then
  echo "release-check: FAIL: scripts/release/build.sh passes build tags or GOFLAGS" >&2
  exit 1
fi
if grep -nE -- 'build .*-tags' Makefile | grep -v '^[0-9]*:#'; then
  echo "release-check: FAIL: a Makefile build passes build tags" >&2
  exit 1
fi
# The trusted-sender test build turns off the approval guard: never in a release.
if grep -n -- 'trusttest' scripts/release/build.sh .github/workflows/release.yml; then
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

has() { grep -aqF -- "$2" "$1"; }

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
