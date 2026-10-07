import { ArrowUp, Folder, Plus } from 'lucide-react'
import { useState, type ComponentProps, type ReactNode } from 'react'
import { NewAgentDialog } from './NewAgentDialog'
import { useProject } from '../project/ProjectContext'
import styles from './HolonComposer.module.css'

type Props = {
  contextLabel: string
  contextPrompt: string
  branch?: string
  icon?: ReactNode
  issueId?: string
  onSubmit?: ComponentProps<typeof NewAgentDialog>['onSubmit']
}

// Keep the chat UI disabled until its backend is ready.
const chatEnabled = false

export function HolonComposer(props: Props) {
  return chatEnabled ? <HolonComposerUI {...props} /> : null
}

function HolonComposerUI({ contextLabel, contextPrompt, branch, icon = <Folder />, issueId, onSubmit }: Props) {
  const project = useProject()
  const [prompt, setPrompt] = useState('')
  const [open, setOpen] = useState(false)
  return <>
    <form className={styles.composer} aria-label="New Holon composer" onSubmit={(event) => { event.preventDefault(); setOpen(true) }}>
      <button type="button" aria-label="New Holon" onClick={() => setOpen(true)}><Plus /></button>
      <span className={styles.context} title={contextPrompt}>{icon}<span>{contextLabel}</span></span>
      <input aria-label="New Holon prompt" placeholder="Ask about this context…" value={prompt} onChange={(event) => setPrompt(event.target.value)} />
      <button className={styles.send} aria-label="Start a Holon" type="submit"><ArrowUp /></button>
    </form>
    <NewAgentDialog open={open} project={project} initialPrompt={prompt} initialContext={contextPrompt} initialBranch={branch} issueId={issueId} onSubmit={onSubmit} title="New Holon" submitLabel="Create Holon" onClose={() => setOpen(false)} />
  </>
}
