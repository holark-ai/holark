export type TerminalTestRuntimeSnapshot = {
  kind: 'presented' | 'candidate'
  columns: number
  rows: number
  cursor_x: number
  cursor_y: number
  base_y: number
  viewport_y: number
  buffer_type: string
  viewport: string[]
}

export type TerminalTestRectangle = {
  x: number
  y: number
  width: number
  height: number
}

export type TerminalTestSnapshot = {
  terminal_id: string
  sequence: number
  columns: number
  rows: number
  proposed_dimensions?: { columns: number; rows: number }
  container_rectangle?: TerminalTestRectangle
  screen_rectangle?: TerminalTestRectangle
  pending_render_completion: boolean
  pending_render_bytes: number
  render_epoch: number
  restoring: boolean
  status: string
  connected: boolean
  process_ready: boolean
  consumer_stale: boolean
  runtimes: TerminalTestRuntimeSnapshot[]
}

type Snapshotter = () => TerminalTestSnapshot

const snapshotters = new Map<string, Snapshotter>()

export function registerTerminalTestSnapshot(terminalID: string, snapshotter: Snapshotter) {
  if (import.meta.env.VITE_HOLARK_TERMINAL_E2E !== '1') return () => {}
  window.__HOLARK_TERMINAL_TEST_SNAPSHOTS__ ??= { snapshots }
  snapshotters.set(terminalID, snapshotter)
  return () => { snapshotters.delete(terminalID) }
}

function snapshots() {
  return [...snapshotters.values()].map((snapshotter) => snapshotter())
}
