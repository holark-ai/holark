import { useEffect, useRef, useState } from 'react'
import type { HolonPreparation, PreparationState, PublishTarget } from '../features/terminal/HolonPublishControls'

// Demonstrates the shared controls without starting a CLI or changing a repository.
export function usePreviewPreparation(onGeneration: () => void, onViewGeneration: () => void): HolonPreparation {
  const [state, setState] = useState<PreparationState>('idle')
  const [dirty, setDirty] = useState(true)
  const [message, setMessage] = useState('')
  const [publishedLabel, setPublishedLabel] = useState('')
  const pendingTarget = useRef<PublishTarget | null>(null)
  useEffect(() => {
    if (!['generating', 'committing', 'publishing'].includes(state)) return
    const timer = window.setTimeout(() => {
      if (state === 'generating') {
        setMessage((current) => current || 'Guard terminal persistence for unnamed workspaces\n\nAvoid saving terminal selections without a workspace identifier.\nUpdate the workspace preview image.')
        setState(pendingTarget.current ? 'committing' : 'ready')
      } else if (state === 'committing') {
        setDirty(false)
        setState(pendingTarget.current ? 'publishing' : 'ready')
      } else {
        const target = pendingTarget.current
        setPublishedLabel(`${target === 'draft' ? 'Draft PR' : target === 'wip' ? 'WIP PR' : 'PR'} published at ${new Date().toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })} · preview only`)
        setState('published')
      }
    }, state === 'generating' ? 10000 : 700)
    return () => window.clearTimeout(timer)
  }, [state])
  return {
    state, dirty, message, publishedLabel,
    onMessageChange: setMessage,
    onGenerate: () => { pendingTarget.current = null; onGeneration(); setState('generating') },
    onViewGeneration,
    onCommit: () => { if (message.trim()) { pendingTarget.current = null; setState('committing') } },
    onPublish: (target) => {
      pendingTarget.current = target
      if (!dirty) setState('publishing')
      else if (message.trim()) setState('committing')
      else { onGeneration(); setState('generating') }
    },
    onCancel: () => { pendingTarget.current = null; setState(message ? 'ready' : 'idle') },
  }
}
