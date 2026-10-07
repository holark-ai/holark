/// <reference types="vite/client" />
/// <reference types="vitest/globals" />

type TerminalRendererE2E = {
  block(): void
  release(): void
  beforeWrite(): Promise<void>
  afterWrite?(): Promise<void>
  stallNextCompletion?(): void
  stalled?(): Promise<void>
}

type LaunchResult = { status: 'ready' } | { status: 'error'; reason: 'invalid-token' | 'connection' }

interface Window {
  __HOLARK_LAUNCH_READY__?: Promise<LaunchResult>
  __HOLARK_TERMINAL_RENDERER_E2E__?: TerminalRendererE2E
  __HOLARK_TERMINAL_VISIBLE_HISTORY__?: string[]
  __HOLARK_TERMINAL_TEST_SNAPSHOTS__?: {
    snapshots(): import('./features/terminal/terminalTestSnapshots').TerminalTestSnapshot[]
  }
}
