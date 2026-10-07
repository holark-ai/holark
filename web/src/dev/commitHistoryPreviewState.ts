export const commitHistoryPreview = { delayMs: 600, failNextPage: false, newCommit: false }

export function resetCommitHistoryPreview() {
  Object.assign(commitHistoryPreview, { delayMs: 600, failNextPage: false, newCommit: false })
}
