import { Terminal } from '@xterm/xterm'
import { TerminalClipboardProvenance } from './terminalClipboard'

const encoder = new TextEncoder()
const payload = 'c;aGVsbG8='
const terminals: Terminal[] = []
afterEach(() => { for (const terminal of terminals.splice(0)) terminal.dispose() })

function setup() {
  const terminal = new Terminal({ allowProposedApi: true })
  terminals.push(terminal)
  const tracker = new TerminalClipboardProvenance(terminal)
  const requests: string[] = []
  terminal.parser.registerOscHandler(52, (data) => {
    if (tracker.available()) requests.push(data)
    return true
  })
  const write = (data: string | Uint8Array, eligible: boolean) => {
    tracker.write(eligible)
    return new Promise<void>((resolve) => terminal.write(typeof data === 'string' ? encoder.encode(data) : data, resolve))
  }
  return { tracker, requests, write }
}

it.each(['\x07', '\x1b\\', '\u009c'])('tracks eligibility at every command split (%j)', async (terminator) => {
  const command = `\x1b]52;${payload}${terminator}`
  // xterm dispatches at ESC, before the final backslash of ST.
  const dispatchLength = command.length - (terminator === '\x1b\\' ? 1 : 0)
  for (let split = 1; split < dispatchLength; split++) {
    for (const eligible of [false, true]) {
      const { write, requests } = setup()
      await write(command.slice(0, split), eligible)
      await write(command.slice(split), true)
      expect(requests, `split ${split}`).toEqual(eligible ? [payload] : [])
    }
  }
})

it('allows eligible split commands and rejects an inactive middle write', async () => {
  for (const eligible of [false, true]) {
    const { write, requests } = setup()
    await write('\x1b]52;c;', true)
    await write('aGVs', eligible)
    await write('bG8=\x07', true)
    expect(requests).toEqual(eligible ? [payload] : [])
  }
})

it('keeps restored commands separate from later live commands in the same write', async () => {
  const { write, requests } = setup()
  await write(`\x1b]52;${payload}`, false)
  await write(`\x07\x1b]52;${payload}\x07`, true)
  expect(requests).toEqual([payload])
})

it('uses xterm boundaries for other OSCs, cancellations and malformed UTF-8', async () => {
  const { write, requests } = setup()
  await write(new Uint8Array([0x1b, 0xff]), false)
  await write(`]52;${payload}\x07`, true)
  expect(requests).toEqual([])
  await write('\x1b]0;restored title', false)
  await write(`\x07\x1b]52;cancelled\x18\x1b]052;${payload}\x07`, true)
  expect(requests).toEqual([payload])
})

it('rejects a UTF-8 OSC introducer split between inactive and active writes', async () => {
  const { write, requests } = setup()
  const command = encoder.encode(`\u009d52;${payload}\x07`)
  await write(command.slice(0, 1), false)
  await write(command.slice(1), true)
  expect(requests).toEqual([])
})

it('invalidates commands when the terminal becomes inactive during parsing', async () => {
  const { write, requests, tracker } = setup()
  await write(`\x1b]52;${payload}`, true)
  tracker.invalidate()
  await write('\x07', true)
  expect(requests).toEqual([])
  await write(`\x1b]52;${payload}\x07`, true)
  expect(requests).toEqual([payload])
})
