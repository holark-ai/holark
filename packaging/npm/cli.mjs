#!/usr/bin/env node
import { spawn } from 'node:child_process';
import { constants } from 'node:os';
import { delimiter, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

if (!['linux', 'darwin'].includes(process.platform) || !['x64', 'arm64'].includes(process.arch)) {
  console.error(`holark: unsupported platform ${process.platform}-${process.arch}. Supported: Linux and macOS on x64 and ARM64.`);
  process.exit(1);
}

const packageDirectory = dirname(fileURLToPath(import.meta.url));
const child = spawn(join(packageDirectory, 'native', `${process.platform}-${process.arch}`, 'holark'), process.argv.slice(2), {
  stdio: 'inherit',
  env: {
    ...process.env,
    HOLARK_XTERM_WORKER_DIR: process.env.HOLARK_XTERM_WORKER_DIR || join(packageDirectory, 'terminal-worker'),
    // Use the same Node installation for the worker as for this launcher.
    PATH: [dirname(process.execPath), process.env.PATH].filter(Boolean).join(delimiter),
  },
});

for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(signal, () => child.kill(signal));
}

child.on('error', (error) => {
  console.error(`holark: could not start the bundled executable: ${error.message}`);
  process.exitCode = 1;
});

child.on('exit', (code, signal) => {
  process.exitCode = signal ? 128 + constants.signals[signal] : (code ?? 1);
});
