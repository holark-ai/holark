export type TerminalSocketFactory = (url: string) => WebSocket
export type TerminalAvailability = 'available' | 'missing' | 'retry'
export type TerminalAvailabilityProbe = (url: string) => Promise<TerminalAvailability>

export const terminalProtocolVersion = 12

export type TerminalDimensions = { columns: number; rows: number }

export type TerminalAttachment = {
  token: string
}

export type TerminalCheckpoint = {
  terminal_id: string
  sequence: number
  dimensions: TerminalDimensions
  format_version: 'holark.ansi.v1'
  replay_payload: string
  checksum: string
  quality: 'trusted' | 'degraded'
}

export type ProcessNotification = {
  terminal_id: string
  sequence: number
  kind: 'output' | 'resize' | 'exit'
  data?: string
  characters?: number
  dimensions?: TerminalDimensions
  resize_revision?: number
  exit_code?: number
}

export type TerminalRestore = {
  attachment: TerminalAttachment
  checkpoint: TerminalCheckpoint
  tail?: ProcessNotification[]
  last_sequence: number
}

export type TerminalUpdateStream = {
  terminal_id: string
  checkpoint?: TerminalCheckpoint
  notifications?: ProcessNotification[]
  last_sequence: number
}

export type TerminalDeliveryCode =
  'connection_unavailable' | 'terminal_lost' | 'terminal_not_found' | 'terminal_ended' |
  'stale_attachment' | 'terminal_not_running' | 'stale_resize' | 'terminal_failed' | 'attachment_transferred'

export type DeliveryFrame = {
  version: number
  type: 'attached' | 'updates' | 'result' | 'error' | 'retired'
  request_id?: string
  restore?: TerminalRestore
  update?: TerminalUpdateStream
  code?: TerminalDeliveryCode
  message?: string
}

export type AttachmentState = {
  terminal_id: string
}

export function parseDeliveryFrame(data: unknown): DeliveryFrame | null {
  if (typeof data !== 'string') return null
  try {
    const frame = JSON.parse(data) as Partial<DeliveryFrame>
    if (frame.version !== terminalProtocolVersion ||
      (frame.type !== 'attached' && frame.type !== 'updates' && frame.type !== 'result' &&
      frame.type !== 'error' && frame.type !== 'retired')) return null
    return frame as DeliveryFrame
  } catch {
    return null
  }
}

export function validDimensions(columns: number, rows: number) {
  return Number.isInteger(columns) && Number.isInteger(rows) && columns >= 20 && columns <= 500 && rows >= 5 && rows <= 300
}

export function decodeBytes(encoded: unknown) {
  // Older terminal hosts encoded an empty Go byte slice as JSON null.
  if (encoded === null) return new Uint8Array()
  if (typeof encoded !== 'string') return null
  try {
    const binary = atob(encoded)
    return Uint8Array.from(binary, (character) => character.charCodeAt(0))
  } catch {
    return null
  }
}

export function encodeBytes(bytes: Uint8Array) {
  let binary = ''
  const chunkSize = 0x8000
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunkSize))
  }
  return btoa(binary)
}
