import type { TerminalSize } from '../../data/types'

const MIN_COLUMNS = 80
const MAX_COLUMNS = 160
const MIN_ROWS = 20
const MAX_ROWS = 60

const RAIL_WIDTH = 56
const OPERATIONS_PANEL_WIDTH = 112
const PAGE_HORIZONTAL_PADDING = 56
const PAGE_VERTICAL_PADDING = 56
const TERMINAL_FRAME_HORIZONTAL_INSET = 36
const TERMINAL_FRAME_VERTICAL_INSET = 22
const TERMINAL_HEADER_HEIGHT = 128
const TERMINAL_CELL_WIDTH = 8
const TERMINAL_CELL_HEIGHT = 17

export function estimateAgentTerminalSize(viewport: Pick<Window, 'innerWidth' | 'innerHeight'> = window): TerminalSize {
  const terminalWidth = viewport.innerWidth - RAIL_WIDTH - OPERATIONS_PANEL_WIDTH - PAGE_HORIZONTAL_PADDING - TERMINAL_FRAME_HORIZONTAL_INSET
  const terminalHeight = viewport.innerHeight - PAGE_VERTICAL_PADDING - TERMINAL_HEADER_HEIGHT - TERMINAL_FRAME_VERTICAL_INSET

  return {
    columns: clamp(Math.floor(terminalWidth / TERMINAL_CELL_WIDTH), MIN_COLUMNS, MAX_COLUMNS),
    rows: clamp(Math.floor(terminalHeight / TERMINAL_CELL_HEIGHT), MIN_ROWS, MAX_ROWS),
  }
}

function clamp(value: number, minimum: number, maximum: number) {
  return Math.max(minimum, Math.min(maximum, value))
}
