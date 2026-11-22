#!/usr/bin/env node
// Puts the `wy` binary the app bundles into resources/wy/<os>-<arch>/ (electron-builder's
// extraResources picks it up from there). Two modes:
//
//   node scripts/fetch-wy.mjs <os> <arch>
//       go build of ../cmd/wy with CGO_ENABLED=0 and the release ldflags of
//       scripts/release/build.sh. Environment: GO (default go), WY_VERSION (default: the
//       version in package.json).
//   node scripts/fetch-wy.mjs <os> <arch> --from <dir>
//       copy from the output of scripts/release/build.sh (<dir> holds wy_<ver>_<os>_<arch>.tar.gz
//       or .zip and SHA256SUMS); the archive is checked against SHA256SUMS first.
//
// <os> is linux, mac (or darwin) or win (or windows); <arch> is x64 (amd64) or arm64; either
// can be `auto` for the machine running the script (CI's package job uses `auto auto`).
// The folder uses electron-builder's names (`${os}-${arch}`: linux|mac|win, x64|arm64).
// Nothing is downloaded and nothing is published.

import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, chmodSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const OS = { linux: ['linux', 'linux'], mac: ['mac', 'darwin'], darwin: ['mac', 'darwin'], win: ['win', 'windows'], windows: ['win', 'windows'] };
const ARCH = { x64: ['x64', 'amd64'], amd64: ['x64', 'amd64'], arm64: ['arm64', 'arm64'] };
const GO_PKG = 'github.com/mraj602/watchyard/internal/version';

/** Maps CLI names to the builder folder name and the Go names. Throws on an unknown name. */
export function resolveTarget(os, arch, host = { platform: process.platform, arch: process.arch }) {
  const hostOs = { linux: 'linux', darwin: 'mac', win32: 'win' }[host.platform];
  const o = OS[os === 'auto' ? hostOs : os];
  const a = ARCH[arch === 'auto' ? host.arch : arch];
  if (!o) throw new Error(`unknown os "${os}" (linux, mac, win)`);
  if (!a) throw new Error(`unknown arch "${arch}" (x64, arm64)`);
  return { builderOs: o[0], goos: o[1], builderArch: a[0], goarch: a[1], bin: o[1] === 'windows' ? 'wy.exe' : 'wy' };
}

/** `-s -w -X ...Version=<ver> -X ...Commit=<commit>`, as scripts/release/build.sh. */
export function ldflags(version, commit) {
  return `-s -w -X ${GO_PKG}.Version=${version} -X ${GO_PKG}.Commit=${commit}`;
}

/** Looks `file` up in the text of a SHA256SUMS file. Returns the hex digest or null. */
export function sumFor(sums, file) {
  for (const line of sums.split('\n')) {
    const m = /^([0-9a-f]{64})\s+\*?(.+)$/.exec(line.trim());
    if (m && m[2] === file) return m[1];
  }
  return null;
}

function run(cmd, args, opts = {}) {
  const r = spawnSync(cmd, args, { stdio: ['ignore', 'inherit', 'inherit'], ...opts });
  if (r.error) throw r.error;
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(' ')} exited ${r.status}`);
}

function main(argv) {
  const args = [...argv];
  const fromAt = args.indexOf('--from');
  let from = null;
  if (fromAt >= 0) {
    from = args[fromAt + 1];
    if (!from) throw new Error('--from needs a folder');
    args.splice(fromAt, 2);
  }
  if (args.length !== 2) throw new Error('usage: fetch-wy.mjs <os> <arch> [--from <release dir>]');
  const t = resolveTarget(args[0], args[1]);

  const appDir = fileURLToPath(new URL('..', import.meta.url));
  const repoRoot = resolve(appDir, '..');
  const pkg = JSON.parse(readFileSync(join(appDir, 'package.json'), 'utf8'));
  const out = join(appDir, 'resources', 'wy', `${t.builderOs}-${t.builderArch}`);
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });

  if (!from) {
    const version = (process.env.WY_VERSION || pkg.version).replace(/^v/, '');
    const git = spawnSync('git', ['rev-parse', '--short', 'HEAD'], { cwd: repoRoot, encoding: 'utf8' });
    const commit = git.status === 0 ? git.stdout.trim() : 'unknown';
    // No build flag comes in from outside, as in scripts/release/build.sh: GOFLAGS is cleared,
    // and one set with `go env -w` (which an empty variable does not override) stops the build.
    const go = process.env.GO || 'go';
    const env = { ...process.env, CGO_ENABLED: '0', GOFLAGS: '', GOOS: t.goos, GOARCH: t.goarch };
    const cfg = spawnSync(go, ['env', 'GOFLAGS'], { cwd: repoRoot, env, encoding: 'utf8' });
    if (cfg.status !== 0) throw new Error('go env GOFLAGS failed');
    if (cfg.stdout.trim() !== '') throw new Error(`go env sets GOFLAGS to ${cfg.stdout.trim()}; the bundled wy takes no extra flags (go env -u GOFLAGS)`);
    run(go, ['build', '-trimpath', '-ldflags', ldflags(version, commit), '-o', join(out, t.bin), './cmd/wy'], { cwd: repoRoot, env });
  } else {
    const dir = resolve(from);
    const suffix = `_${t.goos}_${t.goarch}.`;
    const archive = readdirSync(dir).find((f) => f.startsWith('wy_') && f.includes(suffix) && /\.(tar\.gz|zip)$/.test(f));
    if (!archive) throw new Error(`no wy archive for ${t.goos}_${t.goarch} in ${dir}`);
    const want = sumFor(readFileSync(join(dir, 'SHA256SUMS'), 'utf8'), archive);
    if (!want) throw new Error(`${archive} is not listed in SHA256SUMS`);
    const got = createHash('sha256').update(readFileSync(join(dir, archive))).digest('hex');
    if (got !== want) throw new Error(`${archive}: checksum mismatch (SHA256SUMS ${want}, file ${got})`);
    const tmp = mkdtempSync(join(tmpdir(), 'fetch-wy-'));
    try {
      // tar reads both .tar.gz and .zip on Linux, macOS (bsdtar) and Windows 10+ (bsdtar).
      run('tar', ['-xf', join(dir, archive), '-C', tmp, t.bin]);
      cpSync(join(tmp, t.bin), join(out, t.bin));
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  }
  if (t.goos !== 'windows') chmodSync(join(out, t.bin), 0o755);
  if (!existsSync(join(out, t.bin))) throw new Error('wy was not produced');
  console.log(`fetch-wy: ${join(out, t.bin)}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main(process.argv.slice(2));
  } catch (e) {
    console.error(`fetch-wy: ${e.message}`);
    process.exit(1);
  }
}
