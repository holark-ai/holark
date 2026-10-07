import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  timeout: 45_000,
  expect: {
    timeout: 10_000,
  },
  use: {
    baseURL: 'http://127.0.0.1:18180',
    trace: 'retain-on-failure',
  },
  webServer: {
    env: { SHELL: '/bin/sh' },
    command: 'VITE_HOLARK_TERMINAL_E2E=1 npm --prefix web run build && go run ./internal/browsere2e/fixture --listen 127.0.0.1:18180 --static-dir internal/localshell/frontend/dist',
    cwd: '..',
    url: 'http://127.0.0.1:18180/__e2e/state',
    timeout: 120_000,
    reuseExistingServer: false,
  },
  projects: [
    {
      name: 'chromium',
      testIgnore: 'terminal-stale-reconnect.spec.ts',
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'firefox',
      testMatch: 'terminal-stale-reconnect.spec.ts',
      use: { ...devices['Desktop Firefox'] },
    },
  ],
})
