import { useState } from 'react'
import { RepositoryView } from '../features/repository/RepositoryView'
import { commitHistoryPreview } from './commitHistoryPreviewState'
import { previewRepositoryCommits } from './repositoryPreviewData'
import styles from './RepositoryHistoryPreview.module.css'

export function RepositoryHistoryPreview() {
  const [notice, setNotice] = useState('')
  return <>
    <RepositoryView mode="tree" />
    <details className={styles.controls}>
      <summary>Commit history preview</summary>
      <div>
        <strong>Long commit history</strong>
        <p>{previewRepositoryCommits.length - 1} commits on main, loaded 50 at a time. Scroll near the bottom to load more.</p>
        <label>Page delay <select defaultValue={commitHistoryPreview.delayMs} onChange={event => { commitHistoryPreview.delayMs = Number(event.target.value) }}>
          <option value={0}>None</option><option value={600}>600 ms</option><option value={2000}>2 seconds</option>
        </select></label>
        <button type="button" onClick={() => { commitHistoryPreview.failNextPage = true; setNotice('The next older-page request will fail. Use Retry in the list to recover.') }}>Fail next page</button>
        <button type="button" disabled={commitHistoryPreview.newCommit} onClick={() => { commitHistoryPreview.newCommit = true; setNotice('A new commit is available on main. The next history poll will offer to refresh (within 7 seconds).') }}>Simulate new commit on main</button>
        {notice && <p role="status">{notice}</p>}
        <p>Reload to reset. Select any commit in Changes to inspect its diff.</p>
      </div>
    </details>
  </>
}
