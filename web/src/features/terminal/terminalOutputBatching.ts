export const terminalWriteBatchBytes = 64 * 1024

export function shouldFlushTerminalOutputBatch(currentBytes: number, nextBytes: number) {
  return currentBytes > 0 && currentBytes + nextBytes > terminalWriteBatchBytes
}

export function combineTerminalOutput(chunks: Uint8Array[]) {
  const bytes = chunks.reduce((total, chunk) => total + chunk.length, 0)
  const data = new Uint8Array(bytes)
  let offset = 0
  for (const chunk of chunks) {
    data.set(chunk, offset)
    offset += chunk.length
  }
  return data
}
