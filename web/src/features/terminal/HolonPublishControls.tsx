import { ChevronDown, X } from 'lucide-react'
import { useId } from 'react'
import { DismissiblePopoverGroup, useDismissiblePopover } from '../../components/DismissiblePopover'
import { SplitButtonMenu, SplitButtonMenuItem } from '../../components/SplitButtonMenu'
import styles from './HolonPublishControls.module.css'

export type PublishTarget = 'open' | 'draft' | 'wip'
export type PreparationState = 'idle' | 'generating' | 'ready' | 'committing' | 'publishing' | 'published' | 'failed'
export type HolonPreparation = {
  state: PreparationState
  dirty: boolean
  message: string
  error?: string
  publishedLabel?: string
  onMessageChange: (message: string) => void
  onGenerate: () => void
  onViewGeneration: () => void
  onCommit: () => void
  onPublish: (target: PublishTarget) => void
  onCancel: () => void
}

export function CommitMessageControls({ preparation: p }: { preparation: HolonPreparation }) {
  const id = useId()
  if (!p.dirty) return null
  const busy = ['generating', 'committing', 'publishing'].includes(p.state)
  return <div className={styles.commit}>
    {p.state === 'generating' ? <div className={styles.commitActions}><button onClick={p.onViewGeneration}>Generating… ↗</button><button aria-label="Cancel message generation" onClick={p.onCancel}><X /></button></div>
      : !p.message && p.state !== 'ready' ? <button onClick={p.onGenerate} disabled={busy}>{p.state === 'failed' ? 'Retry generation' : 'Generate message'}</button>
        : <><label htmlFor={id}>Commit message</label><textarea id={id} value={p.message} disabled={busy} onChange={(event) => p.onMessageChange(event.target.value)} rows={4} />
          <div className={styles.commitActions}><button onClick={p.onViewGeneration}>View generation ↗</button><button className={styles.validate} onClick={p.onCommit} disabled={busy || !p.message.trim()}>{p.state === 'committing' ? 'Committing…' : 'Validate commit'}</button></div></>}
  </div>
}

export function HolonPublishControls({ preparation }: { preparation: HolonPreparation }) {
  return <DismissiblePopoverGroup><PublishActions preparation={preparation} /></DismissiblePopoverGroup>
}

function PublishActions({ preparation: p }: { preparation: HolonPreparation }) {
  const { open, rootRef, triggerRef, toggle, close } = useDismissiblePopover('publish-options')
  const busy = ['generating', 'committing', 'publishing'].includes(p.state)
  const label = (target: PublishTarget) => `${p.dirty ? 'Auto-commit & publish' : 'Publish'} ${target === 'open' ? 'PR' : target === 'draft' ? 'draft PR' : 'WIP PR'}`
  return <footer className={styles.publish}>
    {p.state === 'published' ? <span role="status">{p.publishedLabel || 'PR published'}</span> : <>
      {p.error && <small role="alert">{p.error}</small>}
      <div className={`${styles.split} ${p.dirty ? '' : styles.neutral}`} ref={rootRef} onKeyDown={(event) => {
        if (event.key === 'Escape' && open) { event.preventDefault(); event.stopPropagation(); close(); triggerRef.current?.focus() }
      }}>
        <button className={styles.primary} disabled={p.state === 'committing' || p.state === 'publishing'} onClick={() => p.state === 'generating' ? p.onViewGeneration() : p.onPublish('open')}>
          {p.state === 'generating' ? 'Generating… ↗' : p.state === 'committing' ? 'Committing…' : p.state === 'publishing' ? 'Publishing…' : label('open')}
        </button>
        {p.state === 'generating' ? <button aria-label="Cancel publishing" onClick={p.onCancel}><X /></button> : !busy && <button ref={triggerRef} aria-label="More publishing actions" aria-expanded={open} onClick={toggle}><ChevronDown /></button>}
        {open && !busy && <SplitButtonMenu className={styles.options}>{(['draft', 'wip'] as const).map((target) => <SplitButtonMenuItem key={target} onClick={() => { close(); p.onPublish(target) }}>{label(target)}</SplitButtonMenuItem>)}</SplitButtonMenu>}
      </div>
      {p.dirty && !busy && <p className={styles.commitHint}>Or commit first, then publish</p>}
    </>}
  </footer>
}
