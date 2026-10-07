import {
  parseDeliveryFrame, terminalProtocolVersion, type AttachmentState, type DeliveryFrame,
  type TerminalAvailabilityProbe, type TerminalSocketFactory,
} from './terminalProtocol'

type TransportOptions = {
  url: string
  socketFactory: TerminalSocketFactory
  availabilityProbe?: TerminalAvailabilityProbe
  attachment: () => AttachmentState
  onFrame: (frame: DeliveryFrame, epoch: number) => void
  onConnectionPending: () => void
  onConnectionClosed: (epoch: number) => void
  onProtocolError: () => void
  onTerminalNotFound: () => void
}

// TerminalTransport owns only the attach/reconnect protocol. It deliberately
// knows nothing about xterm, DOM mounting, focus, or terminal presentation.
export class TerminalTransport {
  private socket: WebSocket | null = null
  private epoch = 0
  private reconnectAttempts = 0
  private reconnectTimer?: number
  private disposed = false
  private suspended = false
  private probeGeneration = 0

  constructor(private readonly options: TransportOptions) {
    this.connect()
  }

  isCurrent(epoch: number) {
    return !this.disposed && this.epoch === epoch
  }

  markReady(epoch: number) {
    if (this.epoch === epoch) this.reconnectAttempts = 0
  }

  ready() {
    return !this.disposed && !this.suspended && this.socket?.readyState === WebSocket.OPEN
  }

  send(frame: object, epoch?: number) {
    if (epoch !== undefined && epoch !== this.epoch) return false
    const socket = this.socket
    if (!socket || socket.readyState !== WebSocket.OPEN) return false
    socket.send(JSON.stringify(frame))
    return true
  }

  restart(epoch: number) {
    if (this.epoch !== epoch) return
    const socket = this.socket
    this.socket = null
    this.epoch += 1
    socket?.close()
    this.scheduleReconnect()
  }

  reconnect(epoch: number) {
    if (this.epoch !== epoch || this.socket !== null) return
    this.scheduleReconnect()
  }

  suspend(epoch: number) {
    if (this.epoch !== epoch) return
    this.suspended = true
    this.probeGeneration += 1
    this.epoch += 1
    if (this.reconnectTimer !== undefined) window.clearTimeout(this.reconnectTimer)
    this.reconnectTimer = undefined
    const socket = this.socket
    this.socket = null
    socket?.close()
  }

  dispose() {
    if (this.disposed) return
    this.disposed = true
    this.probeGeneration += 1
    if (this.reconnectTimer !== undefined) window.clearTimeout(this.reconnectTimer)
    this.reconnectTimer = undefined
    const socket = this.socket
    this.socket = null
    socket?.close()
  }

  private connect() {
    if (this.disposed || this.suspended) return
    this.options.onConnectionPending()
    const availabilityProbe = this.options.availabilityProbe
    if (!availabilityProbe) {
      this.openSocket()
      return
    }
    const generation = ++this.probeGeneration
    void availabilityProbe(this.options.url).then((availability) => {
      if (this.disposed || this.suspended || generation !== this.probeGeneration) return
      if (availability === 'missing') {
        this.suspended = true
        this.options.onTerminalNotFound()
        return
      }
      if (availability === 'retry') {
        this.warn('terminal_availability_retry')
        this.scheduleReconnect()
        return
      }
      this.openSocket()
    }).catch((error: unknown) => {
      if (this.disposed || this.suspended || generation !== this.probeGeneration) return
      this.warn('terminal_availability_failed', { error_name: error instanceof Error ? error.name : typeof error })
      this.scheduleReconnect()
    })
  }

  private openSocket() {
    if (this.disposed || this.suspended) return
    let socket: WebSocket
    try {
      socket = this.options.socketFactory(this.options.url)
    } catch (error) {
      this.warn('websocket_creation_failed', { error_name: error instanceof Error ? error.name : typeof error })
      this.scheduleReconnect()
      return
    }
    this.socket = socket
    this.epoch += 1
    const epoch = this.epoch
    const current = () => !this.disposed && this.socket === socket && this.epoch === epoch
    socket.onopen = () => {
      if (!current()) return
      this.reconnectAttempts = 0
      const attachment = this.options.attachment()
      this.send({
        version: terminalProtocolVersion,
        type: 'attach',
        payload: attachment,
      }, epoch)
    }
    socket.onmessage = (event) => {
      if (!current()) return
      const frame = parseDeliveryFrame(event.data)
      if (!frame) {
        this.warn('websocket_protocol_error', { epoch })
        this.options.onProtocolError()
        socket.close()
        return
      }
      this.options.onFrame(frame, epoch)
    }
    socket.onerror = () => {
      if (current()) this.warn('websocket_error', { epoch, ready_state: socket.readyState })
      if (current()) this.options.onConnectionPending()
    }
    socket.onclose = (event) => {
      if (!current()) return
      this.warn('websocket_closed', { epoch, code: event.code, reason: event.reason, was_clean: event.wasClean })
      this.socket = null
      this.options.onConnectionPending()
      this.options.onConnectionClosed(epoch)
    }
  }

  private scheduleReconnect() {
    if (this.disposed || this.suspended || this.reconnectTimer !== undefined) return
    this.reconnectAttempts += 1
    const delay = Math.min(10_000, 250 * (2 ** Math.min(this.reconnectAttempts - 1, 6)))
    this.options.onConnectionPending()
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = undefined
      this.connect()
    }, delay)
  }

  private warn(event: string, fields: Record<string, unknown> = {}) {
    const details = { terminal_id: this.options.attachment().terminal_id, epoch: this.epoch, ...fields }
    console.warn('[Holark terminal connection]', { event, ...details })
  }
}
