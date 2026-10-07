import { Buffer } from 'node:buffer';
import { access } from 'node:fs/promises';
import { join } from 'node:path';
import { stdin, stdout } from 'node:process';
import { createInterface } from 'node:readline';
import headlessPackage from '@xterm/headless';
import serializePackage from '@xterm/addon-serialize';
import unicode11Package from '@xterm/addon-unicode11';

const { Terminal } = headlessPackage;
const { SerializeAddon } = serializePackage;
const { Unicode11Addon } = unicode11Package;

const models = new Map();
let closed = false;
let chain = Promise.resolve();

const requests = createInterface({ input: stdin, crlfDelay: Infinity });

requests.on('line', (line) => {
  let message;
  try {
    message = JSON.parse(line);
  } catch (error) {
    respond(undefined, false, { error: `invalid JSON: ${error.message}` });
    return;
  }
  chain = chain.then(() => handle(message)).catch((error) => {
    respond(message.id, false, { error: error.message, error_category: error.category });
  });
});

requests.on('close', () => {
  closeWorker();
});

async function handle(message) {
  if (!message || typeof message !== 'object') {
    throw new Error('request must be an object');
  }
  await waitForTestGate(message.command);
  switch (message.command) {
    case 'create':
      createModel(message);
      respond(message.id, true, {});
      break;
    case 'init':
      createModel({ ...message, model: '__legacy' });
      respond(message.id, true, {});
      break;
    case 'write':
      await writeBytes(requireModel(message), message.data, message.sequence);
      respond(message.id, true, { reset_sequence: requireModel(message).resetSequence });
      break;
    case 'resize':
      resizeModel(requireModel(message), message);
      respond(message.id, true, { reset_sequence: requireModel(message).resetSequence });
      break;
    case 'snapshot': {
      const model = requireModel(message);
      respond(message.id, true, { data: snapshotBase64(model), reset_sequence: model.resetSequence });
      break;
    }
    case 'inspect':
      respond(message.id, true, inspectTerminal(requireModel(message)));
      break;
    case 'destroy':
      destroyModel(message.model);
      respond(message.id, true, {});
      break;
    case 'close':
      closeWorker();
      respond(message.id, true, {});
      break;
    default:
      throw new Error(`unknown command ${JSON.stringify(message.command)}`);
  }
}

function resizeModel(model, message) {
  assertPositiveInteger(message.columns, 'columns');
  assertPositiveInteger(message.rows, 'rows');
  assertNextSequence(model, message.sequence);
  model.terminal.resize(message.columns, message.rows);
  model.lastSequence = message.sequence;
}

async function waitForTestGate(command) {
  const directory = process.env.HOLARK_XTERM_E2E_CONTROL_DIR;
  if (!directory || (command !== 'write' && command !== 'resize' && command !== 'snapshot')) {
    return;
  }
  const gate = join(directory, 'paused');
  while (true) {
    try {
      await access(gate);
    } catch {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
}

function createModel(message) {
  if (typeof message.model !== 'string' || message.model.length === 0 || message.model.length > 256) {
    throw new Error('model identity is required');
  }
  if (models.has(message.model)) {
    throw new Error('terminal model already exists');
  }
  assertPositiveInteger(message.columns, 'columns');
  assertPositiveInteger(message.rows, 'rows');
  assertPositiveInteger(message.scrollback, 'scrollback');
  assertReplayLimit(message.maximum_replay_bytes);
  const terminal = new Terminal({
    cols: message.columns,
    rows: message.rows,
    scrollback: message.scrollback,
    scrollOnEraseInDisplay: true,
    allowProposedApi: true
  });
  terminal.loadAddon(new Unicode11Addon());
  terminal.unicode.activeVersion = '11';
  const serializer = new SerializeAddon();
  terminal.loadAddon(serializer);
  const model = {
    terminal,
    serializer,
    scrollbackLines: message.scrollback,
    maximumReplayBytes: message.maximum_replay_bytes,
    decoder: new TextDecoder('utf-8'),
    pendingBytes: Buffer.alloc(0),
    parserBoundaryState: 'ground',
    pendingParserInput: '',
    title: '',
    lastSequence: 0,
    resetSequence: 0
  };
  terminal.onTitleChange((value) => {
    model.title = value;
  });
  models.set(message.model, model);
}

async function writeBytes(model, encoded, sequence) {
  if (typeof encoded !== 'string' || encoded.length === 0) {
    throw new Error('write data is required');
  }
  const incoming = Buffer.from(encoded, 'base64');
  if (incoming.length === 0) {
    throw new Error('write data is empty');
  }
  assertNextSequence(model, sequence);
  const data = model.pendingBytes.length === 0 ? incoming : Buffer.concat([model.pendingBytes, incoming]);
  const complete = completeUTF8PrefixLength(data);
  model.pendingBytes = data.subarray(complete);
  if (complete === 0) {
    model.lastSequence = sequence;
    return;
  }
  const text = model.decoder.decode(data.subarray(0, complete), { stream: true });
  if (text.length === 0) {
    model.lastSequence = sequence;
    return;
  }
  await new Promise((resolve) => model.terminal.write(text, resolve));
  trackParserBoundary(model, text, sequence);
  model.lastSequence = sequence;
}

function snapshotBase64(model) {
  const parserInput = Buffer.from(model.pendingParserInput, 'utf8');
  const suffix = parserInput.length === 0 && model.pendingBytes.length === 0
    ? null
    : Buffer.concat([parserInput, model.pendingBytes]);
  const serialize = (scrollback) => {
    const replay = Buffer.from(model.serializer.serialize({ scrollback }) + serializeMouseEncoding(model.terminal), 'utf8');
    return suffix === null ? replay : Buffer.concat([replay, suffix]);
  };

  let payload = serialize(model.scrollbackLines);
  if (payload.length <= model.maximumReplayBytes) {
    return payload.toString('base64');
  }
  payload = serialize(0);
  if (payload.length > model.maximumReplayBytes) {
    const error = new Error('minimum terminal snapshot exceeds the bounded replay payload');
    error.category = 'snapshot_unavailable';
    throw error;
  }
  return payload.toString('base64');
}

function serializeMouseEncoding(terminal) {
  // xterm 6's public modes and SerializeAddon omit mouse encoding. Read the
  // encoding from its mouse service so checkpoint restores preserve the wire
  // format, even when mouse tracking is temporarily disabled. Append this
  // before pending parser input, which may end in an unfinished escape sequence.
  switch (terminal._core.coreMouseService.activeEncoding) {
    case 'SGR': return '\x1b[?1006h';
    case 'SGR_PIXELS': return '\x1b[?1016h';
    case 'DEFAULT': return '\x1b[?1006l';
    default: throw new Error('unsupported terminal mouse encoding');
  }
}

function inspectTerminal(model) {
  const { terminal } = model;
  const buffer = terminal.buffer.active;
  const lines = [];
  for (let index = 0; index < terminal.rows; index += 1) {
    lines.push(buffer.getLine(buffer.baseY + index)?.translateToString(true) ?? '');
  }
  const allLines = [];
  for (let index = 0; index < buffer.length; index += 1) {
    allLines.push(buffer.getLine(index)?.translateToString(true) ?? '');
  }
  return {
    columns: terminal.cols,
    rows: terminal.rows,
    cursorX: buffer.cursorX,
    cursorY: buffer.cursorY,
    baseY: buffer.baseY,
    bufferType: buffer.type,
    title: model.title,
    modes: terminal.modes,
    lines,
    allLines
  };
}

function destroyModel(identity) {
  const model = models.get(identity);
  if (!model) {
    return;
  }
  model.terminal.dispose();
  models.delete(identity);
}

function closeWorker() {
  if (closed) {
    return;
  }
  closed = true;
  for (const model of models.values()) {
    model.terminal.dispose();
  }
  models.clear();
  requests.close();
}

function requireModel(message) {
  const identity = typeof message.model === 'string' ? message.model : '__legacy';
  const model = models.get(identity);
  if (!model || closed) {
    throw new Error('terminal model is unavailable');
  }
  return model;
}

function assertPositiveInteger(value, name) {
  if (!Number.isInteger(value) || value < 1 || value > 100000) {
    throw new Error(`${name} must be a positive integer`);
  }
}

function assertReplayLimit(value) {
  if (!Number.isSafeInteger(value) || value < 1) {
    throw new Error('maximum_replay_bytes must be a positive integer');
  }
}

function assertNextSequence(model, value) {
  if (!Number.isSafeInteger(value) || value <= model.lastSequence) {
    throw new Error('sequence must advance');
  }
}

function completeUTF8PrefixLength(data) {
  if (data.length === 0) {
    return 0;
  }
  let lead = data.length - 1;
  while (lead >= 0 && (data[lead] & 0xc0) === 0x80) {
    lead -= 1;
  }
  if (lead < 0) {
    return data.length;
  }
  const first = data[lead];
  let expected = 1;
  if (first >= 0xc2 && first <= 0xdf) {
    expected = 2;
  } else if (first >= 0xe0 && first <= 0xef) {
    expected = 3;
  } else if (first >= 0xf0 && first <= 0xf4) {
    expected = 4;
  } else {
    return data.length;
  }
  return data.length - lead < expected ? lead : data.length;
}

// The xterm serializer captures completed terminal state but not the parser's
// unfinished input. Track only that incomplete suffix so a checkpoint can put
// a fresh xterm at the same parser boundary before later PTY output is replayed.
function trackParserBoundary(model, text, sequence) {
  for (const character of text) {
    trackParserCharacter(model, character, sequence);
  }
}

function trackParserCharacter(model, character, sequence) {
  const codepoint = character.codePointAt(0);

  if (model.parserBoundaryState === 'ground') {
    startParserSequence(model, character, codepoint);
    return;
  }

  if (model.parserBoundaryState === 'osc' || model.parserBoundaryState === 'string') {
    if ((model.parserBoundaryState === 'osc' && codepoint === 0x07) || codepoint === 0x9c) {
      finishParserSequence(model);
    } else if (codepoint === 0x18 || codepoint === 0x1a) {
      finishParserSequence(model);
    } else if (codepoint === 0x1b) {
      model.pendingParserInput += character;
      model.parserBoundaryState = model.parserBoundaryState === 'osc' ? 'osc-escape' : 'string-escape';
    } else if (!isC0OrDelete(codepoint)) {
      model.pendingParserInput += character;
    }
    return;
  }

  if (model.parserBoundaryState === 'osc-escape' || model.parserBoundaryState === 'string-escape') {
    if (character === '\\') {
      finishParserSequence(model);
      return;
    }
    // ESC aborts the previous string when it is not followed by the ST final.
    // Continue tracking that ESC as the start of the replacement sequence.
    if (codepoint === 0x1b) {
      model.pendingParserInput = character;
      model.parserBoundaryState = 'escape';
      return;
    }
    if (startParserSequence(model, character, codepoint)) {
      return;
    }
    if (isC0OrDelete(codepoint)) {
      model.pendingParserInput = '\x1b';
      model.parserBoundaryState = 'escape';
      return;
    }
    model.pendingParserInput = `\x1b${character}`;
    model.parserBoundaryState = 'escape';
    trackEscapeCharacter(model, character, codepoint);
    return;
  }

  if (codepoint === 0x18 || codepoint === 0x1a) {
    finishParserSequence(model);
    return;
  }
  if (codepoint === 0x1b) {
    model.pendingParserInput = character;
    model.parserBoundaryState = 'escape';
    return;
  }
  if (model.parserBoundaryState === 'escape' && model.pendingParserInput === '\x1b' && character === 'c') {
    model.resetSequence = sequence;
  }
  if (startParserSequence(model, character, codepoint)) {
    return;
  }
  // C0 controls execute immediately without changing the surrounding parser
  // state. Their effects are already serialized, so replaying them would apply
  // those effects twice. DEL is ignored and likewise need not be retained.
  if (isC0OrDelete(codepoint)) {
    return;
  }

  model.pendingParserInput += character;
  switch (model.parserBoundaryState) {
    case 'escape':
    case 'escape-intermediate':
      trackEscapeCharacter(model, character, codepoint);
      break;
    case 'csi':
      if (codepoint >= 0x40 && codepoint <= 0x7e) {
        finishParserSequence(model);
      } else if (codepoint >= 0x20 && codepoint <= 0x2f) {
        model.parserBoundaryState = 'csi-intermediate';
      } else if (!isC0OrDelete(codepoint) && !(codepoint >= 0x30 && codepoint <= 0x3f)) {
        model.parserBoundaryState = 'csi-ignore';
      }
      break;
    case 'csi-intermediate':
      if (codepoint >= 0x40 && codepoint <= 0x7e) {
        finishParserSequence(model);
      } else if (codepoint >= 0x30 && codepoint <= 0x3f) {
        model.parserBoundaryState = 'csi-ignore';
      } else if (!isC0OrDelete(codepoint) && !(codepoint >= 0x20 && codepoint <= 0x2f)) {
        model.parserBoundaryState = 'csi-ignore';
      }
      break;
    case 'csi-ignore':
      if (codepoint >= 0x40 && codepoint <= 0x7e) {
        finishParserSequence(model);
      }
      break;
  }
}

function startParserSequence(model, character, codepoint) {
  let state;
  switch (codepoint) {
    case 0x1b:
      state = 'escape';
      break;
    case 0x90:
    case 0x98:
    case 0x9e:
    case 0x9f:
      state = 'string';
      break;
    case 0x9b:
      state = 'csi';
      break;
    case 0x9d:
      state = 'osc';
      break;
    default:
      return false;
  }
  model.pendingParserInput = character;
  model.parserBoundaryState = state;
  return true;
}

function trackEscapeCharacter(model, character, codepoint) {
  if (model.parserBoundaryState === 'escape-intermediate') {
    if (codepoint >= 0x30 && codepoint <= 0x7e) {
      finishParserSequence(model);
    } else if (!isC0OrDelete(codepoint) && !(codepoint >= 0x20 && codepoint <= 0x2f)) {
      finishParserSequence(model);
    }
    return;
  }

  switch (character) {
    case '[':
      model.parserBoundaryState = 'csi';
      return;
    case ']':
      model.parserBoundaryState = 'osc';
      return;
    case 'P':
    case 'X':
    case '^':
    case '_':
      model.parserBoundaryState = 'string';
      return;
  }
  if (codepoint >= 0x20 && codepoint <= 0x2f) {
    model.parserBoundaryState = 'escape-intermediate';
  } else if ((codepoint >= 0x30 && codepoint <= 0x7e) || !isC0OrDelete(codepoint)) {
    finishParserSequence(model);
  }
}

function finishParserSequence(model) {
  model.pendingParserInput = '';
  model.parserBoundaryState = 'ground';
}

function isC0OrDelete(codepoint) {
  return codepoint <= 0x1f || codepoint === 0x7f;
}

function respond(id, ok, fields) {
  stdout.write(`${JSON.stringify({ id, ok, ...fields })}\n`);
}
