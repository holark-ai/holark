#!/usr/bin/env node
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, delimiter, dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repository = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const registry = 'https://registry.npmjs.org/';
const manifest = JSON.parse(readFileSync(join(repository, 'packaging/npm/package.json'), 'utf8'));

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: repository, stdio: 'inherit', ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} failed${result.signal ? ` (${result.signal})` : ` (exit ${result.status})`}.`);
  return result;
}

function release() {
  const args = process.argv.slice(2);
  if (args.length === 1 && ['--help', '-h'].includes(args[0])) {
    console.log(`Usage: node packaging/npm/release.mjs VERSION [--publish] [--tag TAG]

Build all four platforms, write a checksum, check a temporary npm installation,
and run npm publish --dry-run. Add --publish to publish the checked archive.
The default npm tag is latest. The source package version is never changed.`);
    return;
  }

  const version = args.shift();
  const match = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/.exec(version ?? '');
  if (!match || match[0] !== version || match.slice(1, 4).some(part => !Number.isSafeInteger(Number(part))) ||
      match[4]?.split('.').some(part => /^0\d+$/.test(part))) {
    throw new Error('Specify a version such as 0.1.1 or 0.1.1-rc.1. Run with --help for usage.');
  }

  let publish = false;
  let tag = 'latest';
  while (args.length) {
    const option = args.shift();
    if (option === '--publish' && !publish) publish = true;
    else if (option === '--tag' && args.length) tag = args.shift();
    else throw new Error(`Unknown or incomplete option: ${option}`);
  }
  if (!/^[A-Za-z][A-Za-z0-9_-]*$/.test(tag) || tag.includes('\n')) throw new Error('Use an npm tag such as latest or next.');
  if (!['linux', 'darwin'].includes(process.platform) || !['x64', 'arm64'].includes(process.arch)) {
    throw new Error('Run this script on Linux or macOS, using x64 or ARM64 Node.js.');
  }

  if (publish) {
    console.log('Checking npm login and version availability...');
    run('npm', ['whoami', '--registry', registry]);
    const existing = spawnSync('npm', ['view', `${manifest.name}@${version}`, 'version', '--json', '--registry', registry], {
      cwd: repository, encoding: 'utf8', maxBuffer: 1024 * 1024,
    });
    if (existing.error) throw existing.error;
    if (existing.status === 0) throw new Error(`${manifest.name}@${version} is already published. Choose an unused version.`);
    let errorCode;
    try { errorCode = JSON.parse(existing.stdout).error?.code; } catch {}
    if (errorCode !== 'E404') throw new Error('Could not check npm version availability. Check your registry connection and login.');
  }

  console.log(`Building ${manifest.name}@${version} for Linux and macOS, x64 and ARM64...`);
  run('make', ['package-npm', `NPM_VERSION=${version}`]);
  const archive = join(repository, '.artifacts/npm', `${manifest.name}-${version}.tgz`);
  const checksum = createHash('sha256').update(readFileSync(archive)).digest('hex');
  writeFileSync(`${archive}.sha256`, `${checksum}  ${basename(archive)}\n`);

  console.log('Checking the archive in a temporary global npm installation...');
  const temporary = mkdtempSync(join(tmpdir(), 'holark-release-'));
  try {
    const prefix = join(temporary, 'prefix');
    run('npm', ['install', '--global', '--prefix', prefix, '--cache', join(temporary, 'cache'),
      '--offline', '--ignore-scripts', '--no-audit', '--no-fund', archive], { cwd: temporary });
    const installed = join(prefix, 'lib/node_modules', manifest.name);
    const installedManifest = JSON.parse(readFileSync(join(installed, 'package.json'), 'utf8'));
    if (installedManifest.version !== version) throw new Error('The installed package version does not match the requested version.');
    const env = { ...process.env, PATH: [join(prefix, 'bin'), dirname(process.execPath), process.env.PATH].filter(Boolean).join(delimiter) };
    delete env.HOLARK_XTERM_WORKER_DIR;
    run('holark', ['--help'], { cwd: temporary, env });
    // Starting with closed stdin checks the worker's bundled imports and shutdown.
    run(process.execPath, [join(installed, 'terminal-worker/worker.mjs')], {
      cwd: temporary, env, stdio: ['ignore', 'inherit', 'inherit'], timeout: 30000,
    });
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }

  const publishArgs = [archive, '--access', 'public', '--tag', tag, '--registry', registry];
  console.log('Checking npm publication with a dry run...');
  run('npm', ['publish', '--dry-run', ...publishArgs]);
  console.log(`Archive: ${archive}\nChecksum: ${archive}.sha256`);
  if (publish) {
    console.log(`Publishing ${manifest.name}@${version} with tag ${tag}...`);
    run('npm', ['publish', ...publishArgs]);
  } else {
    console.log('Package checked. Nothing published. Publish this archive with:');
    console.log(`npm publish ./.artifacts/npm/${basename(archive)} --access public --tag ${tag} --registry ${registry}`);
  }
}

try {
  release();
} catch (error) {
  console.error(`holark release: ${error.message}`);
  process.exitCode = 1;
}
