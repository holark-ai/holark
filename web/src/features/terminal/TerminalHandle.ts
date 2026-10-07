import { FitAddon } from '@xterm/addon-fit'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { Terminal, type IDisposable, type ITheme } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import styles from './HolonTerminal.module.css'
import { TerminalTransport } from './TerminalTransport'
import { TerminalClipboardProvenance } from './terminalClipboard'
import {
  decodeBytes, encodeBytes, terminalProtocolVersion, validDimensions, type DeliveryFrame,
  type TerminalAttachment, type TerminalAvailabilityProbe, type TerminalSocketFactory, type TerminalUpdateStream,
} from './terminalProtocol'
import { terminalThemeFromTokens, terminalTypographyForClient } from './terminalTheme'
import {
  combineTerminalOutput, shouldFlushTerminalOutputBatch, terminalWriteBatchBytes,
} from './terminalOutputBatching'
import {
  registerTerminalTestSnapshot,
  type TerminalTestRuntimeSnapshot, type TerminalTestSnapshot,
} from './terminalTestSnapshots'

export type HandleOptions = {
  label: string
  inputEnabled: boolean
  resizeEnabled: boolean
  focusSuppressed: boolean
  active: boolean
  onTerminalFocus?: () => void
}

export type HandleState = {
  status: string
  restoring: boolean
  message: string
  restorationQuality: 'trusted' | 'degraded'
  degradedWarningDismissed: boolean
}

type TerminalRuntime = {
  terminal: Terminal
  fit: FitAddon
  host: HTMLDivElement
  input: IDisposable[]
  clipboardHandler?: IDisposable
  clipboardProvenance: TerminalClipboardProvenance
  scrollbarActivity?: IDisposable
}

export const initialHandleState: HandleState = {
  status: 'Connecting...',
  restoring: true,
  message: '',
  restorationQuality: 'trusted',
  degradedWarningDismissed: false,
}

// Scrolling in OpenCode produces bursts of screen updates that could exceed
// the previous 100 KB queue limit and trigger recovery during normal use.
// Allow up to 1 MiB of pending output to absorb those bursts; the progress
// watchdog still detects stalled writes. This limit does not control scrollback.
const terminalConsumerQueueBytes = 1024 * 1024
const terminalRenderProgressTimeoutMs = 1_500
const terminalClipboardMaxBytes = 1024 * 1024

// TerminalHandle owns one long-lived xterm instance and its process-facing
// state. React surfaces only mount or park its wrapper, like VS Code reparents
// a terminal instance without reconstructing its model on navigation.
export class TerminalHandle {
  readonly id: string
  readonly wrapper: HTMLDivElement

  private readonly parking: HTMLElement
  private readonly transport: TerminalTransport
  private runtime: TerminalRuntime
  private candidate?: TerminalRuntime
  private candidateRestorationQuality?: 'trusted' | 'degraded'
  private sequence = 0
  private attachment?: TerminalAttachment
  private connected = false
  private processReady = false
  private pendingRenderBytes = 0
  private pendingIncrementalRenderBytes = 0
  private pendingRenderCompletions = 0
  private consumerStale = false
  private resizeRevision = 0
  private lastResizeColumns = 0
  private lastResizeRows = 0
  private lastResizeRevision = 0
  private resizeTimer?: number
  private fitFrame?: number
  private restorationFrame?: number
  private renderProgressTimer?: number
  private resizeObserver?: ResizeObserver
  private mountedAt?: HTMLElement
  private disposed = false
  private ended = false
  private focused = false
  private restoreFocusAfterStale = false
  private hasCommitted = false
  private renderEpoch = 0
  private deliveryRevision = 0
  private pendingOutputDelivery?: {
    update: TerminalUpdateStream & { notifications: NonNullable<TerminalUpdateStream['notifications']> }
    epoch: number
    renderEpoch: number
    bytes: number
    completions: number
    revision: number
    clipboardEligible: boolean
  }
  private writeTail = Promise.resolve()
  private options: HandleOptions
  private theme: ITheme
  private state: HandleState = initialHandleState
  private readonly listeners = new Set<() => void>()
  private readonly unregisterTestSnapshot: () => void

  constructor(id: string, url: string, parking: HTMLElement, socketFactory: TerminalSocketFactory, availabilityProbe: TerminalAvailabilityProbe | undefined, options: HandleOptions, theme: ITheme) {
    this.id = id
    this.parking = parking
    this.options = options
    this.theme = theme
    this.wrapper = document.createElement('div')
    this.wrapper.className = styles.terminal
    this.wrapper.style.setProperty('--terminal-runtime-bg', theme.background ?? '')
    this.wrapper.dataset.terminalHandle = id
    this.wrapper.setAttribute('aria-label', options.label)
    this.wrapper.addEventListener('focusin', this.focusIn)
    this.wrapper.addEventListener('focusout', this.focusOut)
    parking.appendChild(this.wrapper)
    this.runtime = this.createRuntime(theme)
    this.unregisterTestSnapshot = registerTerminalTestSnapshot(this.id, this.testSnapshot)
    this.setRuntimeVisibility(this.runtime, false)
    this.transport = new TerminalTransport({
      url,
      socketFactory,
      availabilityProbe,
      attachment: () => ({ terminal_id: this.id }),
      onFrame: (frame, epoch) => this.receive(frame, epoch),
      onConnectionPending: () => {
        this.connected = false
        this.processReady = false
        this.attachment = undefined
        this.updateState({ status: this.connectionStatus(), restoring: !this.ended, message: '' })
      },
      onConnectionClosed: (epoch) => {
        void this.writeTail.finally(() => {
          if (this.ended) this.transport.suspend(epoch)
          else this.transport.reconnect(epoch)
        })
      },
      onProtocolError: () => this.updateState({ message: 'Terminal protocol response was invalid.' }),
      onTerminalNotFound: () => {
        this.connected = false
        this.processReady = false
        this.attachment = undefined
        this.ended = true
        this.clearRenderProgressWatchdog()
        this.updateState({ status: 'Not found', restoring: false, message: 'Terminal not found.' })
      },
    })
  }

  snapshot = () => this.state

  subscribe = (listener: () => void) => {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }

  update(options: HandleOptions, theme: ITheme) {
    const previous = this.options
    this.options = options
    // Diff review keeps the terminal active and mounted, but suppresses focus.
    // Invalidate unfinished clipboard commands when leaving the terminal too.
    if (!options.active || options.focusSuppressed) {
      this.runtime.clipboardProvenance.invalidate()
      this.candidate?.clipboardProvenance.invalidate()
    }
    if (options.label !== previous.label) this.wrapper.setAttribute('aria-label', options.label)
    this.updateTheme(theme)
    this.syncInteraction()
    this.setRuntimeVisibility(this.runtime, options.active && this.hasCommitted)
    if (this.candidate) this.setRuntimeVisibility(this.candidate, false)
    if (options.active && (!previous.active || (options.resizeEnabled && !previous.resizeEnabled))) this.scheduleFit()
  }

  updateTheme(theme: ITheme) {
    if (this.theme === theme) return
    this.theme = theme
    this.runtime.terminal.options.theme = theme
    this.wrapper.style.setProperty('--terminal-runtime-bg', theme.background ?? '')
    if (this.candidate) this.candidate.terminal.options.theme = theme
  }

  mount(target: HTMLElement) {
    this.mountedAt = target
    target.appendChild(this.wrapper)
    this.resizeObserver?.disconnect()
    this.resizeObserver = new ResizeObserver((entries) => {
      const entry = entries[entries.length - 1]
      if (!entry || entry.contentRect.width <= 0 || entry.contentRect.height <= 0) return
      this.scheduleFit()
    })
    this.resizeObserver.observe(target)
    this.scheduleFit()
    if (this.focused && this.options.active) requestAnimationFrame(() => this.focus())
  }

  park(target: HTMLElement) {
    if (this.mountedAt !== target) return
    this.mountedAt = undefined
    this.runtime.clipboardProvenance.invalidate()
    this.candidate?.clipboardProvenance.invalidate()
    this.resizeObserver?.disconnect()
    this.resizeObserver = undefined
    this.parking.appendChild(this.wrapper)
  }

  focus() {
    if (!this.options.active || !this.options.inputEnabled || this.options.focusSuppressed || this.state.restoring) return
    if (document.querySelector('[role="dialog"][aria-modal="true"]')) return
    this.runtime.terminal.focus()
  }

  dismissDegradedWarning() {
    if (this.state.restorationQuality !== 'degraded') return
    this.updateState({ degradedWarningDismissed: true })
    window.requestAnimationFrame(() => this.focus())
  }

  private clipboardAvailable() {
    return !this.disposed && this.options.active && Boolean(this.mountedAt) &&
      !this.options.focusSuppressed && !this.state.restoring && !this.ended &&
      this.connected && this.processReady && this.transport.ready()
  }

  private async copyOSC52(runtime: TerminalRuntime, data: string) {
    if (runtime !== this.runtime || !runtime.clipboardProvenance.available() || !this.clipboardAvailable() ||
      !window.isSecureContext || !navigator.clipboard?.writeText ||
      !document.hasFocus() || !this.wrapper.contains(document.activeElement)) return
    const separator = data.indexOf(';')
    if (separator < 0 || !/^[cps0-7]*$/.test(data.slice(0, separator))) return
    const encoded = data.slice(separator + 1)
    // Reject reads ("?"), whitespace, malformed base64 and oversized data before decoding.
    if (encoded.length > Math.ceil(terminalClipboardMaxBytes / 3) * 4 ||
      !/^[A-Za-z0-9+/]*={0,2}$/.test(encoded)) return
    let text: string
    try {
      const binary = atob(encoded)
      if (binary.length > terminalClipboardMaxBytes) return
      text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(Uint8Array.from(binary, (character) => character.charCodeAt(0)))
    } catch { return }
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      // Clipboard writes are best-effort; never retain failed text for retry.
    }
  }

  dispose() {
    if (this.disposed) return
    this.disposed = true
    if (this.resizeTimer !== undefined) window.clearTimeout(this.resizeTimer)
    if (this.fitFrame !== undefined) window.cancelAnimationFrame(this.fitFrame)
    if (this.restorationFrame !== undefined) window.cancelAnimationFrame(this.restorationFrame)
    this.clearRenderProgressWatchdog()
    this.resizeObserver?.disconnect()
    this.transport.dispose()
    this.unregisterTestSnapshot()
    this.disposeRuntime(this.candidate)
    this.disposeRuntime(this.runtime)
    this.wrapper.removeEventListener('focusin', this.focusIn)
    this.wrapper.removeEventListener('focusout', this.focusOut)
    this.wrapper.remove()
    this.listeners.clear()
  }

  private createRuntime(theme: ITheme): TerminalRuntime {
    const typography = terminalTypographyForClient()
    const terminal = new Terminal({
      allowProposedApi: true,
      allowTransparency: false,
      cursorBlink: false,
      cursorInactiveStyle: 'outline',
      cursorStyle: 'block',
      cursorWidth: 1,
      disableStdin: true,
      drawBoldTextInBrightColors: true,
      fontFamily: typography.fontFamily,
      fontSize: typography.fontSize,
      fontWeight: 'normal',
      fontWeightBold: 'bold',
      letterSpacing: 0,
      lineHeight: typography.lineHeight,
      minimumContrastRatio: 4.5,
      scrollback: 10_000,
      scrollOnEraseInDisplay: true,
      theme,
    })
    terminal.loadAddon(new Unicode11Addon())
    terminal.unicode.activeVersion = '11'
    terminal.loadAddon(new WebLinksAddon((_, uri) => {
      window.open(uri, '_blank', 'noopener,noreferrer')
    }))
    const fit = new FitAddon()
    terminal.loadAddon(fit)
    const host = document.createElement('div')
    host.className = styles.terminal
    host.style.fontFamily = typography.fontFamily
    host.style.fontSize = `${typography.fontSize}px`
    this.wrapper.appendChild(host)
    terminal.open(host)
    const input = [
      terminal.onData((data) => this.input(new TextEncoder().encode(data))),
      // Legacy mouse reports contain raw bytes, including values above ASCII.
      terminal.onBinary((data) => this.input(Uint8Array.from(data, (character) => character.charCodeAt(0)))),
    ]
    const runtime: TerminalRuntime = {
      terminal, fit, host, input, clipboardProvenance: new TerminalClipboardProvenance(terminal),
    }
    runtime.clipboardHandler = terminal.parser.registerOscHandler(52, (data) => {
      // Never return this promise to xterm: permission prompts must not pause parsing.
      void this.copyOSC52(runtime, data)
      return true
    })
    // Match Base UI's 500ms scrolling state before the shared CSS fades the thumb.
    let scrollbarIdleTimer: number | undefined
    const scrollbarScroll = terminal.onScroll(() => {
      host.dataset.scrolling = ''
      window.clearTimeout(scrollbarIdleTimer)
      scrollbarIdleTimer = window.setTimeout(() => {
        delete host.dataset.scrolling
      }, 500)
    })
    runtime.scrollbarActivity = {
      dispose() {
        scrollbarScroll.dispose()
        window.clearTimeout(scrollbarIdleTimer)
      },
    }
    return runtime
  }

  private disposeRuntime(runtime?: TerminalRuntime) {
    if (!runtime) return
    runtime.clipboardHandler?.dispose()
    runtime.clipboardProvenance.dispose()
    runtime.scrollbarActivity?.dispose()
    runtime.input.forEach((listener) => listener.dispose())
    runtime.terminal.dispose()
    runtime.host.remove()
  }

  private receive(frame: DeliveryFrame, epoch: number) {
    if (frame.type === 'retired') {
      this.connected = false
      this.processReady = false
      this.attachment = undefined
      this.transport.suspend(epoch)
      this.updateState({ status: 'Attached elsewhere', restoring: false, message: frame.message || 'This terminal is open in another view.' })
      return
    }
    if (frame.type === 'error') {
      if (frame.code === 'connection_unavailable') {
        this.warnConnectionRestart(epoch, 'server_error', { code: frame.code })
        this.connected = false
        this.processReady = false
        this.attachment = undefined
        this.updateState({
          status: this.connectionStatus(), restoring: true,
          message: this.hasCommitted
            ? frame.message || 'Terminal connection is temporarily unavailable.'
            : '',
        })
        this.transport.restart(epoch)
      } else if (frame.code === 'terminal_lost' || frame.code === 'terminal_not_found' || frame.code === 'terminal_ended') {
        this.connected = false
        this.processReady = false
        this.attachment = undefined
        this.ended = true
        this.clearRenderProgressWatchdog()
        this.transport.suspend(epoch)
        this.updateState({
          status: frame.code === 'terminal_lost' ? 'Lost' : frame.code === 'terminal_ended' ? 'Ended' : 'Not found',
          restoring: false,
          message: frame.message || (frame.code === 'terminal_ended' ? 'Terminal process has ended.' : 'Terminal process is no longer available.'),
        })
      } else if (frame.code === 'stale_attachment') {
        this.connected = false
        this.processReady = false
        this.attachment = undefined
        this.transport.suspend(epoch)
        this.updateState({ status: 'Attached elsewhere', restoring: false, message: frame.message || 'This terminal is open in another view.' })
      } else {
        this.updateState({ message: frame.message || 'Terminal operation failed.' })
      }
      return
    }
    if (frame.type !== 'attached' && frame.type !== 'updates') return
    const restore = frame.restore
    const update = frame.update ? { ...frame.update, notifications: [...(frame.update.notifications ?? [])] } : undefined
    const deliveredCheckpoint = restore?.checkpoint ?? update?.checkpoint
    let checkpoint = deliveredCheckpoint
    const checkpointAlreadyRendered = Boolean(checkpoint && this.hasCommitted && checkpoint.terminal_id === this.id &&
      Number.isSafeInteger(checkpoint.sequence) && checkpoint.sequence <= this.sequence)
    if (checkpointAlreadyRendered) checkpoint = undefined
    if (checkpointAlreadyRendered && deliveredCheckpoint?.quality === 'trusted' && this.state.restorationQuality === 'degraded') {
      this.updateState({ restorationQuality: 'trusted', degradedWarningDismissed: false })
    }
    let renderEpoch = this.renderEpoch
    if (frame.type === 'attached') {
      if (!restore?.attachment.token || restore.checkpoint.format_version !== 'holark.ansi.v1' ||
        !/^[0-9a-f]{64}$/.test(restore.checkpoint.checksum) || !validRestorationQuality(restore.checkpoint.quality)) {
        this.warnConnectionRestart(epoch, 'invalid_restore')
        this.updateState({ message: 'Terminal restore response was invalid.' })
        this.transport.restart(epoch)
        return
      }
      this.transport.markReady(epoch)
      this.connected = true
      this.processReady = true
      this.ended = false
      this.attachment = restore.attachment
      if (!checkpointAlreadyRendered) {
        renderEpoch = ++this.renderEpoch
        this.clearRenderProgressWatchdog()
        this.pendingRenderCompletions = 0
        this.pendingRenderBytes = 0
        this.pendingIncrementalRenderBytes = 0
        this.writeTail = Promise.resolve()
        this.pendingOutputDelivery = undefined
        this.beginCandidate(true)
        this.updateState({ status: this.connectionStatus(), restoring: true, message: '' })
      } else {
        this.updateState({ message: '' })
      }
    }
    const notifications = restore?.tail ?? update?.notifications ?? []
    const renderBytes = frameOutputBytes(frame)
    if (frame.type === 'updates' && renderBytes > 0 &&
      this.pendingIncrementalRenderBytes + renderBytes > terminalConsumerQueueBytes) {
      this.restoreStaleConsumer(epoch, 'queue_overflow', { incoming_bytes: renderBytes })
      return
    }
    this.pendingRenderBytes += renderBytes
    if (frame.type === 'updates') this.pendingIncrementalRenderBytes += renderBytes
    const hasRenderCompletion = Boolean(checkpoint) || renderBytes > 0
    if (hasRenderCompletion) this.pendingRenderCompletions += 1
    if (hasRenderCompletion) this.watchRenderProgress(epoch, renderEpoch)
    const deliveryRevision = ++this.deliveryRevision
    // OpenCode scrolling can queue many small output deliveries. Waiting for an
    // xterm callback after each separate write added enough overhead to fall behind.
    // Combine consecutive output deliveries already waiting, up to 64 KiB, to
    // reduce that overhead without delaying new output to collect a batch.
    const pending = this.pendingOutputDelivery
    const clipboardEligible = this.clipboardAvailable()
    const canBatchOutput = frame.type === 'updates' && update && isContiguousOutputUpdate(update, this.id)
    if (canBatchOutput && pending && pending.clipboardEligible === clipboardEligible &&
      pending.epoch === epoch && pending.renderEpoch === renderEpoch &&
      pending.bytes + renderBytes <= terminalWriteBatchBytes &&
      update.notifications[0].sequence === pending.update.last_sequence + 1) {
      pending.update.notifications.push(...notifications)
      pending.update.last_sequence = update.last_sequence
      pending.bytes += renderBytes
      pending.completions += hasRenderCompletion ? 1 : 0
      pending.revision = deliveryRevision
      return
    }
    // Coalesce only deliveries still waiting for the writer. Checkpoints,
    // resizes and exits remain ordering boundaries, and active writes are immutable.
    const delivery = {
      epoch, renderEpoch, bytes: renderBytes, clipboardEligible,
      completions: hasRenderCompletion ? 1 : 0, revision: deliveryRevision,
    }
    const outputDelivery = canBatchOutput ? Object.assign(delivery, { update }) : undefined
    this.pendingOutputDelivery = outputDelivery
    this.writeTail = this.writeTail.then(async () => {
      if (this.pendingOutputDelivery === outputDelivery) this.pendingOutputDelivery = undefined
      if (this.disposed || renderEpoch !== this.renderEpoch || !this.transport.isCurrent(epoch)) return
      // Earlier deliveries may have rendered past this checkpoint while it was
      // queued. Replaying it now would rewind sequence without replaying the
      // already-delivered tail, leaving restoration waiting for consumed output.
      if (frame.type === 'updates' && checkpoint && checkpoint.terminal_id === this.id &&
        Number.isSafeInteger(checkpoint.sequence) && checkpoint.sequence <= this.sequence) {
        if (checkpoint.quality === 'trusted' && this.state.restorationQuality === 'degraded') {
          this.updateState({ restorationQuality: 'trusted', degradedWarningDismissed: false })
        }
        checkpoint = undefined
      }
      if (frame.type === 'updates' && checkpoint) {
        console.warn('[Holark terminal recovery]', { reason: 'live_checkpoint_restore', terminal_id: this.id, epoch })
        this.beginCandidate(true)
        this.updateState({ status: this.connectionStatus(), restoring: true })
      }
      const terminalRuntime = this.candidate ?? this.runtime
      const terminal = terminalRuntime.terminal
      let sequence = this.sequence
      if (checkpoint) {
        if (checkpoint.terminal_id !== this.id ||
          !Number.isSafeInteger(checkpoint.sequence) || checkpoint.sequence < 0 ||
          !validDimensions(checkpoint.dimensions.columns, checkpoint.dimensions.rows) ||
          checkpoint.format_version !== 'holark.ansi.v1' || !/^[0-9a-f]{64}$/.test(checkpoint.checksum) ||
          !validRestorationQuality(checkpoint.quality)) {
          this.updateState({ message: 'Terminal checkpoint was invalid.' })
          this.transport.restart(epoch)
          return
        }
        const replay = decodeBytes(checkpoint.replay_payload)
        if (!replay) {
          this.updateState({ message: 'Terminal checkpoint payload was invalid.' })
          this.transport.restart(epoch)
          return
        }
        terminal.resize(checkpoint.dimensions.columns, checkpoint.dimensions.rows)
        await writeTerminal(
          terminal,
          replay,
          () => renderEpoch === this.renderEpoch && this.transport.isCurrent(epoch),
          () => terminalRuntime.clipboardProvenance.write(false),
          () => this.watchRenderProgress(epoch, renderEpoch, true),
        )
        if (renderEpoch !== this.renderEpoch || !this.transport.isCurrent(epoch)) return
        // Continue from this checkpoint through its output tail. Previously, recovery
        // always reconnected again here to request a fresher checkpoint, so every queue
        // overflow caused two reconnects even when this restore could catch up.
        this.candidateRestorationQuality = checkpoint.quality
        sequence = checkpoint.sequence
      }
      if (update && update.terminal_id !== this.id) {
        this.updateState({ message: 'Terminal update was invalid.' })
        this.transport.restart(epoch)
        return
      }
      let outputBytes = 0
      let outputChunks: Uint8Array[] = []
      const flushOutput = async () => {
        if (outputBytes === 0) return
        const output = combineTerminalOutput(outputChunks)
        outputBytes = 0
        outputChunks = []
        await writeTerminal(
          terminal,
          output,
          () => renderEpoch === this.renderEpoch && this.transport.isCurrent(epoch),
          () => {
            terminalRuntime.clipboardProvenance.write(
              delivery.clipboardEligible && frame.type === 'updates' && !checkpoint && this.clipboardAvailable())
          },
          () => this.watchRenderProgress(epoch, renderEpoch, true),
        )
        if (renderEpoch !== this.renderEpoch || !this.transport.isCurrent(epoch)) return
      }
      const queueOutput = async (bytes: Uint8Array) => {
        if (shouldFlushTerminalOutputBatch(outputBytes, bytes.length)) await flushOutput()
        outputChunks.push(bytes)
        outputBytes += bytes.length
        if (outputBytes >= terminalWriteBatchBytes) await flushOutput()
      }
      for (const notification of notifications) {
        if (notification.terminal_id !== this.id ||
          !Number.isSafeInteger(notification.sequence)) {
          await flushOutput()
          this.updateState({ message: 'Terminal update sequence was invalid.' })
          this.transport.restart(epoch)
          return
        }
        if (notification.sequence <= sequence) continue
        if (notification.sequence !== sequence + 1) {
          await flushOutput()
          this.updateState({ message: 'Terminal update sequence was invalid.' })
          this.transport.restart(epoch)
          return
        }
        if (notification.kind === 'output') {
          const bytes = decodeBytes(notification.data ?? '')
          if (!bytes || !Number.isInteger(notification.characters) || (notification.characters ?? 0) <= 0) {
            await flushOutput()
            this.updateState({ message: 'Terminal output was invalid.' })
            this.transport.restart(epoch)
            return
          }
          await queueOutput(bytes)
        } else {
          await flushOutput()
          if (notification.kind === 'resize' && notification.dimensions &&
            Number.isSafeInteger(notification.resize_revision) && (notification.resize_revision ?? 0) > 0 &&
            validDimensions(notification.dimensions.columns, notification.dimensions.rows)) {
            terminal.resize(notification.dimensions.columns, notification.dimensions.rows)
            this.rememberResizeDimensions(notification.resize_revision ?? 0, notification.dimensions.columns, notification.dimensions.rows)
            this.resizeRevision = Math.max(this.resizeRevision, notification.resize_revision ?? 0)
            if (!this.candidate) this.scheduleFit()
          } else if (notification.kind === 'exit') {
            if (!Number.isInteger(notification.exit_code)) {
              this.updateState({ message: 'Terminal process update was invalid.' })
              this.transport.restart(epoch)
              return
            }
            this.ended = true
            this.processReady = false
            this.clearRenderProgressWatchdog()
          } else {
            this.updateState({ message: 'Terminal process update was invalid.' })
            this.transport.restart(epoch)
            return
          }
        }
        sequence = notification.sequence
      }
      await flushOutput()
      if (this.disposed || renderEpoch !== this.renderEpoch || !this.transport.isCurrent(epoch)) return
      const lastSequence = restore?.last_sequence ?? update?.last_sequence ?? sequence
      if (!Number.isSafeInteger(lastSequence) || lastSequence < sequence) {
        this.updateState({ message: 'Terminal update barrier was invalid.' })
        this.transport.restart(epoch)
        return
      }
      this.sequence = sequence
      const caughtUp = sequence === lastSequence
      if (caughtUp && delivery.revision === this.deliveryRevision) {
        if (this.candidate) {
          // xterm's write callback advances its model before its DOM renderer.
          // Keep the candidate hidden until the final grid has been painted.
          // Presentation must not hold up writes or their progress watchdog:
          // animation frames pause while the browser tab is hidden.
          terminal.refresh(0, terminal.rows - 1)
          if (this.restorationFrame !== undefined) window.cancelAnimationFrame(this.restorationFrame)
          this.restorationFrame = window.requestAnimationFrame(() => {
            this.restorationFrame = undefined
            // New output can keep draining into this runtime after it is shown.
            // Waiting for a quiet frame would starve presentation during continuous output.
            if (this.disposed || renderEpoch !== this.renderEpoch || !this.transport.isCurrent(epoch) ||
              this.candidate !== terminalRuntime) return
            this.completeRestoration(checkpoint?.quality)
          })
          return
        }
        if (this.candidate || this.state.restoring) this.completeRestoration(checkpoint?.quality)
        else if (this.ended) this.updateState({ status: 'Ended', restoring: false })
      }
    }).finally(() => {
      if (renderEpoch === this.renderEpoch) {
        this.pendingRenderCompletions = Math.max(0, this.pendingRenderCompletions - delivery.completions)
        this.pendingRenderBytes = Math.max(0, this.pendingRenderBytes - delivery.bytes)
        if (frame.type === 'updates') {
          this.pendingIncrementalRenderBytes = Math.max(0, this.pendingIncrementalRenderBytes - delivery.bytes)
        }
        this.watchRenderProgress(epoch, renderEpoch)
      }
    })
  }

  private warnConnectionRestart(epoch: number, reason: string, fields: Record<string, unknown> = {}) {
    const details = {
      terminal_id: this.id, epoch, sequence: this.sequence, reason,
      pending_render_bytes: this.pendingRenderBytes, restoring: this.state.restoring,
      visibility: document.visibilityState, ...fields,
    }
    console.warn('[Holark terminal connection]', { event: 'terminal_connection_restart', ...details })
  }

  private restoreStaleConsumer(
    epoch: number,
    reason: 'queue_overflow' | 'render_timeout',
    details: { incoming_bytes?: number; progress_wait_ms?: number } = {},
  ) {
    if (!this.transport.isCurrent(epoch)) return
    const recovery = {
      terminal_id: this.id, sequence: this.sequence, reason, epoch,
      pending_render_bytes: this.pendingRenderBytes,
      pending_incremental_render_bytes: this.pendingIncrementalRenderBytes,
      pending_render_completions: this.pendingRenderCompletions,
      queue_capacity: terminalConsumerQueueBytes,
      progress_timeout_ms: terminalRenderProgressTimeoutMs,
      restoring: this.state.restoring,
      visibility: document.visibilityState,
      ...details,
    }
    console.warn('[Holark terminal recovery]', recovery)
    this.clearRenderProgressWatchdog()
    this.connected = false
    this.attachment = undefined
    this.pendingRenderBytes = 0
    this.pendingIncrementalRenderBytes = 0
    this.pendingRenderCompletions = 0
    this.consumerStale = true
    this.restoreFocusAfterStale ||= this.focused && this.options.active
    this.renderEpoch += 1
    this.writeTail = Promise.resolve()
    this.pendingOutputDelivery = undefined
    this.setRuntimeVisibility(this.runtime, this.options.active && this.hasCommitted)
    if (this.candidate) this.setRuntimeVisibility(this.candidate, false)
    this.updateState({ status: this.connectionStatus(), restoring: true, message: '' })
    this.transport.restart(epoch)
  }

  private beginCandidate(replace = false) {
    if (this.candidate && !replace) return
    if (this.candidate) this.disposeRuntime(this.candidate)
    this.candidateRestorationQuality = undefined
    const theme = this.runtime.terminal.options.theme ?? terminalThemeFromTokens()
    this.candidate = this.createRuntime(theme)
    this.setRuntimeVisibility(this.runtime, this.options.active && this.hasCommitted)
    this.setRuntimeVisibility(this.candidate, false)
    this.updateState({ restoring: true, status: this.connectionStatus() })
  }

  private connectionStatus() {
    return this.hasCommitted ? 'Reconnecting...' : 'Connecting...'
  }

  // Reattaching to an already-current model still completes restoration, even
  // without a candidate. Ordinary output must not repeat sizing or focus work.
  private completeRestoration(restorationQuality = this.candidateRestorationQuality) {
    const restoreFocus = (this.focused || this.restoreFocusAfterStale) && this.options.active
    if (this.candidate) {
      const previous = this.runtime
      this.runtime = this.candidate
      this.candidate = undefined
      this.disposeRuntime(previous)
    }
    this.hasCommitted = true
    this.consumerStale = false
    this.restoreFocusAfterStale = false
    this.candidateRestorationQuality = undefined
    this.setRuntimeVisibility(this.runtime, this.options.active)
    this.updateState({
      restoring: !this.ended && !this.processReady,
      status: this.ended ? 'Ended' : this.processReady ? 'Connected' : 'Reconnecting...',
      ...(restorationQuality ? { restorationQuality, degradedWarningDismissed: false } : {}),
    })
    this.scheduleFit()
    if (restoreFocus) window.requestAnimationFrame(() => this.focus())
  }

  private input(bytes: Uint8Array) {
    if (!this.options.inputEnabled || this.state.restoring || this.ended || !this.connected || !this.processReady ||
      !this.attachment || !this.transport.ready()) return
    if (bytes.length === 0) return
    if (this.resizeTimer !== undefined) {
      window.clearTimeout(this.resizeTimer)
      this.resizeTimer = undefined
      if (!this.sendResize()) {
        this.connected = false
        this.updateState({ status: 'Reconnecting...', message: 'Input was not sent because the terminal connection was interrupted.' })
        return
      }
    }
    for (let offset = 0; offset < bytes.length; offset += 64 * 1024) {
      const chunk = bytes.subarray(offset, Math.min(offset + 64 * 1024, bytes.length))
      const accepted = this.transport.send({
        version: terminalProtocolVersion, type: 'input', payload: { ...this.attachmentFields(), data_base64: encodeBytes(chunk) },
      })
      if (!accepted) {
        this.connected = false
        this.updateState({ status: 'Reconnecting...', message: 'Input was not sent because the terminal connection was interrupted.' })
        return
      }
    }
  }

  private scheduleFit() {
    if (!this.options.active || !this.mountedAt || this.fitFrame !== undefined) return
    this.fitFrame = window.requestAnimationFrame(() => {
      this.fitFrame = undefined
      if (!this.options.active || !this.mountedAt) return
      const current = this.mountedAt.getBoundingClientRect()
      if (current.width <= 0 || current.height <= 0) return
      const buffer = this.runtime.terminal.buffer.active
      const wasAtBottom = buffer.viewportY === buffer.baseY
      const viewport = buffer.viewportY
      try { this.runtime.fit.fit() } catch { return }
      if (wasAtBottom) this.runtime.terminal.scrollToBottom()
      else this.runtime.terminal.scrollToLine(Math.min(viewport, this.runtime.terminal.buffer.active.baseY))
      const columns = this.runtime.terminal.cols
      const rows = this.runtime.terminal.rows
      if (!this.options.resizeEnabled || !validDimensions(columns, rows) ||
        (columns === this.lastResizeColumns && rows === this.lastResizeRows) || this.resizeTimer !== undefined) return
      this.resizeTimer = window.setTimeout(() => {
        this.resizeTimer = undefined
        this.sendResize()
      }, 75)
    })
  }

  private sendResize() {
    if (!this.options.resizeEnabled || !this.options.active || !this.connected || !this.processReady ||
      !this.attachment || !this.transport.ready()) return true
    const columns = this.runtime.terminal.cols
    const rows = this.runtime.terminal.rows
    if (!validDimensions(columns, rows) ||
      (columns === this.lastResizeColumns && rows === this.lastResizeRows)) return true
    this.resizeRevision += 1
    const sent = this.transport.send({
      version: terminalProtocolVersion, type: 'resize',
      payload: { ...this.attachmentFields(), revision: this.resizeRevision, dimensions: { columns, rows } },
    })
    if (sent) {
      this.rememberResizeDimensions(this.resizeRevision, columns, rows)
    }
    return sent
  }

  private rememberResizeDimensions(revision: number, columns: number, rows: number) {
    if (!Number.isSafeInteger(revision) || revision < this.lastResizeRevision || !validDimensions(columns, rows)) return
    this.lastResizeRevision = revision
    this.lastResizeColumns = columns
    this.lastResizeRows = rows
  }

  private watchRenderProgress(epoch: number, renderEpoch: number, reset = false) {
    if (this.disposed || this.ended || this.pendingRenderCompletions === 0 ||
      renderEpoch !== this.renderEpoch || !this.transport.isCurrent(epoch)) {
      this.clearRenderProgressWatchdog()
      return
    }
    if (this.renderProgressTimer !== undefined && !reset) return
    this.clearRenderProgressWatchdog()
    const progressWaitStarted = performance.now()
    this.renderProgressTimer = window.setTimeout(() => {
      this.renderProgressTimer = undefined
      if (this.disposed || this.ended || this.pendingRenderCompletions === 0 ||
        renderEpoch !== this.renderEpoch || !this.transport.isCurrent(epoch)) return
      this.restoreStaleConsumer(epoch, 'render_timeout', {
        progress_wait_ms: Math.round(performance.now() - progressWaitStarted),
      })
    }, terminalRenderProgressTimeoutMs)
  }

  private clearRenderProgressWatchdog() {
    if (this.renderProgressTimer === undefined) return
    window.clearTimeout(this.renderProgressTimer)
    this.renderProgressTimer = undefined
  }

  private attachmentFields() {
    const attachment = this.attachment
    if (!attachment) return { terminal_id: this.id, attachment: { token: '' } }
    return {
      terminal_id: this.id,
      attachment,
    }
  }

  private updateState(patch: Partial<HandleState>) {
    const next = { ...this.state, ...patch }
    const changed = next.status !== this.state.status || next.restoring !== this.state.restoring ||
      next.message !== this.state.message || next.restorationQuality !== this.state.restorationQuality ||
      next.degradedWarningDismissed !== this.state.degradedWarningDismissed
    if (changed) this.state = next
    // Connection and process readiness can change without changing the UI.
    this.syncInteraction()
    if (!changed) return
    this.listeners.forEach((listener) => listener())
  }

  private syncInteraction() {
    const enabled = this.options.inputEnabled && this.connected && this.processReady && (this.transport?.ready() ?? false) &&
      !!this.attachment && !this.state.restoring && !this.ended
    this.runtime.terminal.options.disableStdin = !enabled
    if (this.candidate) this.candidate.terminal.options.disableStdin = !enabled
  }

  private setRuntimeVisibility(runtime: TerminalRuntime, visible: boolean) {
    runtime.host.style.visibility = visible ? 'visible' : 'hidden'
    runtime.host.style.pointerEvents = visible ? 'auto' : 'none'
  }

  private readonly testSnapshot = (): TerminalTestSnapshot => ({
    terminal_id: this.id,
    sequence: this.sequence,
    columns: this.runtime.terminal.cols,
    rows: this.runtime.terminal.rows,
    proposed_dimensions: proposedDimensions(this.runtime),
    container_rectangle: elementRectangle(this.mountedAt),
    screen_rectangle: elementRectangle(this.runtime.host.querySelector<HTMLElement>('.xterm-screen')),
    pending_render_completion: this.pendingRenderCompletions > 0,
    pending_render_bytes: this.pendingRenderBytes,
    render_epoch: this.renderEpoch,
    restoring: this.state.restoring,
    status: this.state.status,
    connected: this.connected,
    process_ready: this.processReady,
    consumer_stale: this.consumerStale,
    runtimes: [
      terminalRuntimeSnapshot('presented', this.runtime),
      ...(this.candidate ? [terminalRuntimeSnapshot('candidate', this.candidate)] : []),
    ],
  })

  private readonly focusIn = () => {
    this.focused = true
    this.options.onTerminalFocus?.()
  }

  private readonly focusOut = () => {
    window.setTimeout(() => { this.focused = this.wrapper.contains(document.activeElement) }, 0)
  }
}

function validRestorationQuality(value: unknown): value is 'trusted' | 'degraded' {
  return value === 'trusted' || value === 'degraded'
}

function isContiguousOutputUpdate(update: TerminalUpdateStream, terminalID: string) {
  const notifications = update.notifications ?? []
  return update.terminal_id === terminalID && !update.checkpoint && notifications.length > 0 &&
    notifications.every((notification, index) => notification.kind === 'output' &&
      notification.terminal_id === terminalID && Number.isSafeInteger(notification.sequence) &&
      (index === 0 || notification.sequence === notifications[index - 1].sequence + 1)) &&
    update.last_sequence === notifications[notifications.length - 1].sequence
}

function frameOutputBytes(frame: DeliveryFrame) {
  return (frame.restore?.tail ?? frame.update?.notifications ?? []).reduce((total, notification) => {
    if (notification.kind !== 'output' || typeof notification.data !== 'string') return total
    const padding = notification.data.endsWith('==') ? 2 : notification.data.endsWith('=') ? 1 : 0
    return total + Math.max(0, Math.floor(notification.data.length * 3 / 4) - padding)
  }, 0)
}

async function writeTerminal(
  terminal: Terminal,
  data: Uint8Array,
  current: () => boolean,
  started: () => void,
  completed: () => void,
) {
  if (import.meta.env.VITE_HOLARK_TERMINAL_E2E === '1') {
    await window.__HOLARK_TERMINAL_RENDERER_E2E__?.beforeWrite()
  }
  if (!current()) return
  started()
  return new Promise<void>((resolve) => terminal.write(data, async () => {
    if (import.meta.env.VITE_HOLARK_TERMINAL_E2E === '1') {
      await window.__HOLARK_TERMINAL_RENDERER_E2E__?.afterWrite?.()
    }
    completed()
    resolve()
  }))
}

function proposedDimensions(runtime: TerminalRuntime) {
  try {
    const proposed = runtime.fit.proposeDimensions()
    if (!proposed || !validDimensions(proposed.cols, proposed.rows)) return undefined
    return { columns: proposed.cols, rows: proposed.rows }
  } catch {
    return undefined
  }
}

function elementRectangle(element?: Element | null) {
  if (!element) return undefined
  const rectangle = element.getBoundingClientRect()
  return {
    x: rectangle.x,
    y: rectangle.y,
    width: rectangle.width,
    height: rectangle.height,
  }
}

function terminalRuntimeSnapshot(kind: 'presented' | 'candidate', runtime: TerminalRuntime): TerminalTestRuntimeSnapshot {
  const terminal = runtime.terminal
  const buffer = terminal.buffer.active
  const viewport: string[] = []
  for (let row = 0; row < terminal.rows; row += 1) {
    viewport.push(buffer.getLine(buffer.viewportY + row)?.translateToString(true) ?? '')
  }
  return {
    kind,
    columns: terminal.cols,
    rows: terminal.rows,
    cursor_x: buffer.cursorX,
    cursor_y: buffer.cursorY,
    base_y: buffer.baseY,
    viewport_y: buffer.viewportY,
    buffer_type: buffer.type,
    viewport,
  }
}
