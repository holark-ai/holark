import { spawnSync } from 'node:child_process';
import { chmodSync, cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const sourceDirectory = dirname(fileURLToPath(import.meta.url));
const repositoryDirectory = resolve(sourceDirectory, '../..');
const outputDirectory = join(repositoryDirectory, '.artifacts', 'npm');
const packageDirectory = join(outputDirectory, 'package');
const manifest = JSON.parse(readFileSync(join(sourceDirectory, 'package.json'), 'utf8'));
const workerManifest = JSON.parse(readFileSync(join(repositoryDirectory, 'terminal-worker', 'package.json'), 'utf8'));
if (process.argv[2]) manifest.version = process.argv[2];

// npm only includes node_modules when they are declared as bundled dependencies.
// Copy the already-patched worker dependencies so installation needs no scripts.
manifest.dependencies = workerManifest.dependencies;
manifest.bundleDependencies = Object.keys(workerManifest.dependencies);

const targets = [
  { goTarget: 'linux-amd64', npmTarget: 'linux-x64', machine: 62 },
  { goTarget: 'linux-arm64', npmTarget: 'linux-arm64', machine: 183 },
  { goTarget: 'darwin-amd64', npmTarget: 'darwin-x64', machine: 0x01000007 },
  { goTarget: 'darwin-arm64', npmTarget: 'darwin-arm64', machine: 0x0100000c },
];

// Check every executable before staging, so a release cannot silently contain
// a host binary under a different platform's name.
for (const target of targets) {
  target.binaryPath = join(repositoryDirectory, 'bin', 'npm', target.goTarget, 'holark');
  const header = readFileSync(target.binaryPath).subarray(0, 20);
  const valid = target.goTarget.startsWith('linux-')
    ? header.subarray(0, 4).toString('hex') === '7f454c46' && header[4] === 2 && header[5] === 1 && header.readUInt16LE(18) === target.machine
    : header.readUInt32LE(0) === 0xfeedfacf && header.readUInt32LE(4) === target.machine;
  if (!valid) throw new Error(`Expected a ${target.npmTarget} executable. Rebuild with make package-npm.`);
}

rmSync(packageDirectory, { recursive: true, force: true });
mkdirSync(join(packageDirectory, 'native'), { recursive: true });
mkdirSync(join(packageDirectory, 'terminal-worker'), { recursive: true });
writeFileSync(join(packageDirectory, 'package.json'), `${JSON.stringify(manifest, null, 2)}\n`);
cpSync(join(sourceDirectory, 'cli.mjs'), join(packageDirectory, 'cli.mjs'));
cpSync(join(sourceDirectory, 'README.md'), join(packageDirectory, 'README.md'));
chmodSync(join(packageDirectory, 'cli.mjs'), 0o755);
for (const target of targets) {
  const destination = join(packageDirectory, 'native', target.npmTarget, 'holark');
  mkdirSync(dirname(destination), { recursive: true });
  cpSync(target.binaryPath, destination);
  chmodSync(destination, 0o755);
}
cpSync(join(repositoryDirectory, 'terminal-worker', 'worker.mjs'), join(packageDirectory, 'terminal-worker', 'worker.mjs'));
cpSync(join(repositoryDirectory, 'terminal-worker', 'node_modules'), join(packageDirectory, 'node_modules'), { recursive: true });

const result = spawnSync('npm', ['pack', '--pack-destination', outputDirectory], {
  cwd: packageDirectory,
  stdio: 'inherit',
});
if (result.error) throw result.error;
process.exitCode = result.status ?? 1;
