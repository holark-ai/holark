export type DiffRow = {
  text: string
  kind: 'added' | 'removed' | 'hunk' | 'context' | 'metadata'
  oldLine?: number
  newLine?: number
  hunk?: string
}

export type DiffExcerptRow = DiffRow & {
  commented: boolean
}

// Stop numbering after malformed content until a fresh, valid hunk header.
// Truncation preserves complete rows, but never makes a trailing fragment actionable.
export function parseUnifiedDiff(diff: string, truncated = false): DiffRow[] {
  const lines = diff.split('\n')
  if (lines.at(-1) === '') lines.pop()
  let oldLine = 0, newLine = 0, oldRemaining = 0, newRemaining = 0
  let hunkStart = -1
  let valid = false
  const rows: DiffRow[] = []
  for (const [index, text] of lines.entries()) {
    const row: DiffRow = { text, kind: 'metadata' }
    rows.push(row)
    const header = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$/.exec(text)
    if (header) {
      oldLine = Number(header[1]); newLine = Number(header[3])
      oldRemaining = Number(header[2] ?? 1); newRemaining = Number(header[4] ?? 1)
      valid = [oldLine, newLine, oldRemaining, newRemaining, oldLine + oldRemaining, newLine + newRemaining].every(Number.isSafeInteger)
        && (oldRemaining === 0 || oldLine > 0) && (newRemaining === 0 || newLine > 0)
      hunkStart = index
      row.kind = 'hunk'
      continue
    }
    if (text === '\\ No newline at end of file') continue
    if (!valid) continue
    if (truncated && index === lines.length - 1 && !diff.endsWith('\n')) { valid = false; continue }
    const prefix = text[0]
    const usesOld = prefix === '-' || prefix === ' '
    const usesNew = prefix === '+' || prefix === ' '
    if ((!usesOld && !usesNew) || (usesOld && oldRemaining <= 0) || (usesNew && newRemaining <= 0)) {
      valid = false
      continue
    }
    if (usesOld) { row.oldLine = oldLine++; oldRemaining-- }
    if (usesNew) { row.newLine = newLine++; newRemaining-- }
    row.kind = prefix === '+' ? 'added' : prefix === '-' ? 'removed' : 'context'
    // Assign the whole displayed hunk after parsing, including complete rows of partial hunks.
    row.hunk = String(hunkStart)
  }
  const starts = rows.flatMap((row, index) => row.kind === 'hunk' ? [index] : [])
  for (const [index, start] of starts.entries()) {
    const end = starts[index + 1] ?? rows.length
    const hunk = lines.slice(start, end).join('\n') + (end < lines.length || diff.endsWith('\n') ? '\n' : '')
    for (let i = start + 1; i < end; i++) if (rows[i].hunk === String(start)) rows[i].hunk = hunk
  }
  return rows
}

// Select context from the persisted hunk only. A duplicated location is
// ambiguous, so callers can fall back to displaying the saved location alone.
export function selectDiffExcerpt(diffHunk: string | undefined, side: 'LEFT' | 'RIGHT', line: number): DiffExcerptRow[] {
  if (!diffHunk || !Number.isSafeInteger(line) || line < 1) return []
  const rows = parseUnifiedDiff(diffHunk).filter((row) => row.hunk !== undefined)
  const matches = rows.flatMap((row, index) => (side === 'LEFT' ? row.oldLine : row.newLine) === line ? [index] : [])
  if (matches.length !== 1) return []

  const selectedIndex = matches[0]
  const selected = rows[selectedIndex]
  return rows.slice(Math.max(0, selectedIndex - 2), selectedIndex + 3)
    .filter((row) => row.hunk === selected.hunk)
    .map((row) => ({ ...row, commented: row === selected }))
}
