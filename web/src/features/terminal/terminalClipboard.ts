import type { Terminal } from '@xterm/xterm'

type OSCParser = { start(): void }
type UTF8Decoder = { interim: Uint8Array }
type InputParser = { currentState: number, _oscParser: OSCParser }

// xterm 6.0's public OSC handler runs only at completion. Observe its internal
// OSC start as well, so provenance covers the identifier and payload without
// duplicating xterm's parsing or UTF-8 decoding. Fail closed if it is unavailable.
export class TerminalClipboardProvenance {
  private readonly parser?: InputParser
  private readonly decoder?: UTF8Decoder
  private readonly originalStart?: () => void
  private writeEligible = false
  private commandEligible = false

  constructor(terminal: Terminal) {
    const input = (terminal as unknown as {
      _core?: { _inputHandler?: { _parser?: InputParser, _utf8Decoder?: UTF8Decoder } }
    })._core?._inputHandler
    this.parser = input?._parser
    this.decoder = input?._utf8Decoder
    if (typeof this.parser?.currentState !== 'number' ||
      typeof this.parser._oscParser?.start !== 'function' || !this.decoder?.interim) return
    const osc = this.parser._oscParser
    this.originalStart = osc.start
    osc.start = () => {
      this.commandEligible = this.writeEligible
      this.originalStart!.call(osc)
    }
  }

  write(eligible: boolean) {
    // ESCAPE is state 1 in xterm 6.0. An ESC from an earlier, ineligible
    // write might introduce an OSC in this write. Conservatively reject that
    // write's commands too, including continuations containing only C0 bytes.
    // The same applies to an unfinished UTF-8 encoding of the C1 introducer.
    const continuation = this.parser?.currentState === 1 || Boolean(this.decoder?.interim[0])
    this.writeEligible = eligible && (!continuation || this.writeEligible)
    this.commandEligible &&= this.writeEligible
  }

  invalidate() {
    this.writeEligible = false
    this.commandEligible = false
  }

  available() {
    return Boolean(this.originalStart) && this.commandEligible
  }

  dispose() {
    if (this.parser && this.originalStart) this.parser._oscParser.start = this.originalStart
  }
}
