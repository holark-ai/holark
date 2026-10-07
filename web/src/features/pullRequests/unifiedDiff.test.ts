import { parseUnifiedDiff, selectDiffExcerpt } from './unifiedDiff'

it('numbers additions, deletions and whitespace-preserving context across hunks', () => {
  const diff = 'diff --git a/old b/new\n--- a/old\n+++ b/new\n@@ -3,2 +5,2 @@ title\n-old\n+\tnew  \n context\n@@ -20 +30 @@\n-before\n+after\n'
  const rows = parseUnifiedDiff(diff)
  expect(rows.slice(0, 4).every((row) => row.oldLine === undefined && row.newLine === undefined)).toBe(true)
  expect(rows[4]).toMatchObject({ text: '-old', kind: 'removed', oldLine: 3 })
  expect(rows[5]).toMatchObject({ text: '+\tnew  ', kind: 'added', newLine: 5 })
  expect(rows[6]).toMatchObject({ text: ' context', oldLine: 4, newLine: 6 })
  expect(rows[8]).toMatchObject({ oldLine: 20, hunk: '@@ -20 +30 @@\n-before\n+after\n' })
  expect(rows[9]).toMatchObject({ newLine: 30 })
  expect(rows[4].hunk).toBe('@@ -3,2 +5,2 @@ title\n-old\n+\tnew  \n context\n')
})

it.each([
  ['@@ -0,0 +1,2 @@\n+a\n+b\n', [[undefined, 1], [undefined, 2]]],
  ['@@ -1,2 +0,0 @@\n-a\n-b\n', [[1, undefined], [2, undefined]]],
  ['@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b', [[1, undefined], [undefined, 1]]],
])('supports new and deleted files, omitted counts and newline metadata: %s', (diff, expected) => {
  expect(parseUnifiedDiff(diff).filter((row) => row.hunk !== undefined).map((row) => [row.oldLine, row.newLine])).toEqual(expected)
})

it('only numbers complete rows in truncated hunks', () => {
  const rows = parseUnifiedDiff('@@ -1,3 +1,3 @@\n a\n-b\n+B\n parti', true)
  expect(rows.filter((row) => row.hunk !== undefined).map((row) => row.text)).toEqual([' a', '-b', '+B'])
  expect(rows.at(-1)).toEqual({ text: ' parti', kind: 'metadata' })
  expect(parseUnifiedDiff('@@ -0,0 +1,2 @@\n+a\n+b\n', true).at(-1)?.newLine).toBe(2)
})

it.each(['', 'Binary files a/a and b/a differ\n', '--- a/a\n+++ b/a\n', '@@ invalid @@\n+no\n', '@@ -0 +1 @@\n+no\n', '@@ -1 +999999999999999999999 @@\n+no\n'])('does not invent locations for unavailable or malformed patches: %s', (diff) => {
  expect(parseUnifiedDiff(diff).every((row) => row.hunk === undefined)).toBe(true)
})

it('stops numbering malformed and excess rows and recovers at the next hunk', () => {
  const rows = parseUnifiedDiff('@@ -1 +1 @@\n a\n+excess\n@@ -3,2 +3,2 @@\n?bad\n context\n@@ -10 +10 @@\n good\n')
  expect(rows.filter((row) => row.hunk !== undefined).map((row) => row.text)).toEqual([' a', ' good'])
})


describe('selectDiffExcerpt', () => {
  it.each([
    ['LEFT' as const, 11, '-removed', [' before', '-removed', '+added', ' after']],
    ['RIGHT' as const, 21, '+added', [' before', '-removed', '+added', ' after', ' last']],
    ['LEFT' as const, 12, ' after', ['-removed', '+added', ' after', ' last']],
    ['RIGHT' as const, 22, ' after', ['-removed', '+added', ' after', ' last']],
  ])('selects additions, deletions and context on the %s side', (side, line, selected, expected) => {
    const excerpt = selectDiffExcerpt('@@ -10,4 +20,4 @@\n before\n-removed\n+added\n after\n last\n', side, line)
    expect(excerpt.map((row) => row.text)).toEqual(expected)
    expect(excerpt.filter((row) => row.commented).map((row) => row.text)).toEqual([selected])
  })

  it('keeps two numbered rows on either side without crossing hunk boundaries', () => {
    const hunk = '@@ -1,6 +1,6 @@\n one\n two\n three\n four\n five\n six\n@@ -20,2 +20,2 @@\n later\n end\n'
    expect(selectDiffExcerpt(hunk, 'RIGHT', 4).map((row) => row.text)).toEqual([' two', ' three', ' four', ' five', ' six'])
    expect(selectDiffExcerpt(hunk, 'RIGHT', 20).map((row) => row.text)).toEqual([' later', ' end'])
  })

  it('supports omitted counts and complete rows from partial hunks while preserving whitespace', () => {
    expect(selectDiffExcerpt('@@ -7 +9 @@\n-\told  \n+  new\t', 'RIGHT', 9)).toMatchObject([
      { text: '-\told  ', oldLine: 7, commented: false },
      { text: '+  new\t', newLine: 9, commented: true },
    ])
    expect(selectDiffExcerpt('@@ -1,4 +1,4 @@\n first\n second', 'RIGHT', 2).map((row) => row.text)).toEqual([' first', ' second'])
  })

  it.each([
    [undefined, 'RIGHT' as const, 1],
    ['', 'RIGHT' as const, 1],
    ['@@ invalid @@\n+line\n', 'RIGHT' as const, 1],
    ['@@ -1 +1 @@\n+line\n', 'LEFT' as const, 1],
    ['@@ -1 +1 @@\n one\n@@ -1 +1 @@\n one\n', 'RIGHT' as const, 1],
  ])('returns no excerpt for missing, malformed, unmatched or ambiguous locations', (hunk, side, line) => {
    expect(selectDiffExcerpt(hunk, side, line)).toEqual([])
  })
})
