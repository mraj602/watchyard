#!/usr/bin/env bash
# make phone-apk-release: the self-hosted Android release build (docs/spec/phone.md P13, P16.1).
#
# You run it, on your laptop; no agent does. It signs with your own keystore, which stays
# outside the repository. Nothing is uploaded, tagged or installed.
#
# Signing input, from your shell or from a properties file outside the repo
# (WY_ANDROID_SIGNING_PROPERTIES=/path/outside/repo/wy-signing.properties with the keys
# storeFile, keyAlias, storePassword, keyPassword); an environment variable wins:
#   WY_ANDROID_KEYSTORE  WY_ANDROID_KEY_ALIAS  WY_ANDROID_KEYSTORE_PASSWORD  [WY_ANDROID_KEY_PASSWORD]
# Without them the build refuses to start. Passwords are passed to Gradle and keytool through
# the environment only and are never printed.
#
# Output in phone/dist/: watchyard-<version>.apk, SHA256SUMS, manifest.json.
# Optional: WY_RELEASE_NOTES (the manifest's notes), WY_GRADLE_ARGS (extra Gradle arguments),
# WY_SKIP_PREBUILD=1 (reuse phone/android, for a repeat after a Gradle failure).
set -euo pipefail
set +x

phone="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
repo="$(cd "$phone/.." && pwd -P)"
cd "$phone"

die() { echo "phone-apk-release: $*" >&2; exit 1; }

# --- signing input -----------------------------------------------------------------------
prop() { # prop <file> <key>: the value after the first '=' of the last matching line
  local line
  line="$(grep -E "^[[:space:]]*$2[[:space:]]*=" "$1" | tail -n 1 || true)"
  line="${line#*=}"
  line="${line#"${line%%[![:space:]]*}"}"
  printf '%s' "${line%"${line##*[![:space:]]}"}"
}
if [ -n "${WY_ANDROID_SIGNING_PROPERTIES:-}" ]; then
  pf="$WY_ANDROID_SIGNING_PROPERTIES"
  [ -f "$pf" ] || die "WY_ANDROID_SIGNING_PROPERTIES is not a file"
  case "$(realpath "$pf")" in "$repo"/*) die "the properties file is inside the repository; keep it in your secrets folder" ;; esac
  : "${WY_ANDROID_KEYSTORE:=$(prop "$pf" storeFile)}"
  : "${WY_ANDROID_KEY_ALIAS:=$(prop "$pf" keyAlias)}"
  : "${WY_ANDROID_KEYSTORE_PASSWORD:=$(prop "$pf" storePassword)}"
  : "${WY_ANDROID_KEY_PASSWORD:=$(prop "$pf" keyPassword)}"
fi
[ -n "${WY_ANDROID_KEYSTORE:-}" ] || die "set WY_ANDROID_KEYSTORE (the path of your release keystore, outside the repo); see phone/README.md"
[ -n "${WY_ANDROID_KEY_ALIAS:-}" ] || die "set WY_ANDROID_KEY_ALIAS"
[ -n "${WY_ANDROID_KEYSTORE_PASSWORD:-}" ] || die "set WY_ANDROID_KEYSTORE_PASSWORD (or use WY_ANDROID_SIGNING_PROPERTIES)"
[ -f "$WY_ANDROID_KEYSTORE" ] || die "WY_ANDROID_KEYSTORE is not a file"
ks="$(realpath "$WY_ANDROID_KEYSTORE")"
case "$ks" in "$repo"/*) die "the keystore is inside the repository; keep it in your secrets folder" ;; esac
export WY_ANDROID_KEYSTORE="$ks" WY_ANDROID_KEY_ALIAS WY_ANDROID_KEYSTORE_PASSWORD
export WY_ANDROID_KEY_PASSWORD="${WY_ANDROID_KEY_PASSWORD:-$WY_ANDROID_KEYSTORE_PASSWORD}"

[ -n "${ANDROID_HOME:-}" ] || die "set ANDROID_HOME to the Android SDK"
command -v keytool >/dev/null || die "keytool (a JDK 17+) is not on PATH"
command -v node >/dev/null || die "node is not on PATH"

# The signing certificate, read from the keystore (the password only through the environment).
# keytool prints its errors on stdout, so its exit status decides, never the output: hashing a
# wrong password's error text once passed as a certificate and the build failed 20 minutes later.
certfile="$(mktemp)"
trap 'rm -f "$certfile"' EXIT
if ! keytool -exportcert -alias "$WY_ANDROID_KEY_ALIAS" -keystore "$ks" \
    -storepass:env WY_ANDROID_KEYSTORE_PASSWORD -file "$certfile" >/dev/null 2>&1 || [ ! -s "$certfile" ]; then
  die "could not read the key from the keystore: check the alias and the keystore password"
fi
cert_sha256="$(sha256sum "$certfile" | cut -d' ' -f1)"
# The private key itself (its password can differ from the store's): the build signs with it.
if ! keytool -certreq -alias "$WY_ANDROID_KEY_ALIAS" -keystore "$ks" \
    -storepass:env WY_ANDROID_KEYSTORE_PASSWORD -keypass:env WY_ANDROID_KEY_PASSWORD >/dev/null 2>&1; then
  die "could not unlock the private key: check WY_ANDROID_KEY_PASSWORD (it defaults to the keystore password)"
fi
# The hub's app mirror only accepts APKs signed with the pinned release certificate.
pinned="$(sed -n 's/^var ReleaseCertSHA256 = "\([0-9a-f]*\)"$/\1/p' "$repo/internal/hub/appmirror/pin.go")"
if [ -n "$pinned" ] && [ "$cert_sha256" != "$pinned" ] && [ -z "${WY_ANDROID_ALLOW_OTHER_KEY:-}" ]; then
  die "the keystore's certificate ($cert_sha256) is not the pinned release certificate ($pinned); installed apps would refuse the update. Set WY_ANDROID_ALLOW_OTHER_KEY=1 only for a deliberate key change"
fi

if [ "${1:-}" = "--preflight" ]; then
  echo "phone-apk-release: signing input ok (key alias $WY_ANDROID_KEY_ALIAS, certificate $cert_sha256)"
  exit 0
fi

# --- build -------------------------------------------------------------------------------
export WY_APP_VARIANT=production
if [ -z "${WY_SKIP_PREBUILD:-}" ]; then
  npx expo prebuild --platform android --no-install --clean
fi
(cd android && ./gradlew --no-daemon -I "$phone/scripts/wy-release-signing.init.gradle" ${WY_GRADLE_ARGS:-} :app:assembleRelease)

built="$phone/android/app/build/outputs/apk/release/app-release.apk"
[ -f "$built" ] || die "Gradle did not produce $built"

# --- verify what was built ---------------------------------------------------------------
# build-tools: the newest installed one.
bt="$(ls -d "$ANDROID_HOME"/build-tools/*/ 2>/dev/null | sort -V | tail -n 1 || true)"
[ -n "$bt" ] && [ -x "${bt}apksigner" ] || die "apksigner not found in \$ANDROID_HOME/build-tools"
signed="$("${bt}apksigner" verify --print-certs "$built")" || die "apksigner refused the APK"
apk_cert="$(printf '%s\n' "$signed" | sed -n 's/^Signer #1 certificate SHA-256 digest: //p' | head -n 1)"
[ "$apk_cert" = "$cert_sha256" ] || die "the APK is not signed with the keystore's key (apksigner says ${apk_cert:-nothing})"
# It must be the release application: the release package id (not the .dev one), no cleartext
# traffic, not debuggable, no backup. Needs aapt2; the release is refused without it.
[ -x "${bt}aapt2" ] || die "aapt2 not found in \$ANDROID_HOME/build-tools (needed to check the manifest)"
want_id="${WY_APP_ID:-io.github.mraj602.watchyard}"
"${bt}aapt2" dump xmltree --file AndroidManifest.xml "$built" | node scripts/check-apk-manifest.mjs "$want_id" \
  || die "the APK is not a release build of $want_id (see above)"
# No proprietary Google code in what ships (phone.md P13).
node scripts/check-no-google.mjs apk "$built" || die "the APK holds proprietary Google classes"

cfg="$(npx expo config --type public --json)"
version="$(printf '%s' "$cfg" | node -p 'JSON.parse(require("fs").readFileSync(0,"utf8")).version')"
build="$(printf '%s' "$cfg" | node -p 'JSON.parse(require("fs").readFileSync(0,"utf8")).android.versionCode')"
min_sdk=31 # the self-hosted profile's minSdkVersion (app.config.ts)
if [ -x "${bt}aapt2" ]; then
  badge="$("${bt}aapt2" dump badging "$built" 2>/dev/null || true)"
  b="$(printf '%s\n' "$badge" | sed -n "s/.*versionCode='\([0-9]*\)'.*/\1/p" | head -n 1)"
  m="$(printf '%s\n' "$badge" | sed -n "s/^sdkVersion:'\([0-9]*\)'.*/\1/p" | head -n 1)"
  [ -z "$b" ] || [ "$b" = "$build" ] || die "the APK's versionCode is $b, the config says $build"
  [ -z "$m" ] || min_sdk="$m"
fi

# --- publish files -----------------------------------------------------------------------
apk="watchyard-$version.apk"
mkdir -p dist
rm -f "dist/$apk" dist/SHA256SUMS dist/manifest.json
cp "$built" "dist/$apk"
(cd dist && sha256sum "$apk" > SHA256SUMS)
apk_sha="$(cut -d' ' -f1 dist/SHA256SUMS)"
node -e '
const [version, build, minSdk, apk, size, sha, cert, notes] = process.argv.slice(1);
const m = { v: 1, version, build: Number(build), min_sdk: Number(minSdk), apk, size: Number(size),
  sha256: sha, cert_sha256: cert, notes, published_at: new Date().toISOString() };
require("fs").writeFileSync("dist/manifest.json", JSON.stringify(m) + "\n");
' "$version" "$build" "$min_sdk" "$apk" "$(wc -c < "dist/$apk" | tr -d ' ')" "$apk_sha" "$cert_sha256" "${WY_RELEASE_NOTES:-}"

echo
echo "release $version (build $build, min sdk $min_sdk)"
echo "  apk          phone/dist/$apk"
echo "  sha256       $apk_sha"
echo "  signing cert $cert_sha256   (publish this with the release)"
echo "  files        phone/dist/SHA256SUMS phone/dist/manifest.json"
echo "next, by hand: gh release create phone-v$version phone/dist/$apk phone/dist/SHA256SUMS phone/dist/manifest.json"
