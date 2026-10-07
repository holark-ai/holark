import { type ReactNode, useRef, useState } from 'react'
import type { PullRequestComment, PullRequestCommentLocation } from '../../data/types'

export function commentLocationLabel(location: Pick<PullRequestComment, 'scope' | 'path' | 'side' | 'line'> | PullRequestCommentLocation) {
  if (location.scope === 'file') return `File: ${location.path}`
  if (location.scope === 'line') return `Line: ${location.path} · ${location.side} ${location.line}`
  return 'General PR comment'
}

export function commentLocationKey(location: PullRequestCommentLocation) {
  return JSON.stringify([location.scope ?? 'pull_request', location.path, location.old_path, location.side, location.line])
}

type Draft = { body: string, posting?: boolean, error?: string, postedId?: string }
export function useCommentDrafts(create: (location: PullRequestCommentLocation, body: string) => Promise<PullRequestComment>) {
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const pending = useRef(new Set<string>())
  const update = (key: string, value: Partial<Draft>) => setDrafts((current) => ({ ...current, [key]: { ...current[key], body: current[key]?.body ?? '', ...value } }))
  return {
    get: (location: PullRequestCommentLocation) => drafts[commentLocationKey(location)] ?? { body: '' },
    change: (location: PullRequestCommentLocation, body: string) => update(commentLocationKey(location), { body, postedId: undefined }),
    discard: (location: PullRequestCommentLocation) => update(commentLocationKey(location), { body: '', error: undefined, postedId: undefined }),
    post: async (location: PullRequestCommentLocation) => {
      const key = commentLocationKey(location)
      const body = drafts[key]?.body.trim()
      if (!body || pending.current.has(key)) return
      pending.current.add(key)
      update(key, { posting: true, error: undefined })
      try {
        const comment = await create(location, body)
        update(key, { body: '', postedId: comment.id })
        return comment
      } catch (error) {
        update(key, { error: error instanceof Error ? error.message : 'Could not post comment.' })
      } finally {
        pending.current.delete(key)
        update(key, { posting: false })
      }
    },
  }
}


export type ScopedComments = {
  activeLocation?: PullRequestCommentLocation
  select: (location: PullRequestCommentLocation) => void
  composer: (location: PullRequestCommentLocation) => ReactNode
}

export function activeComposer(location: PullRequestCommentLocation, comments?: ScopedComments) {
  return comments?.activeLocation && commentLocationKey(comments.activeLocation) === commentLocationKey(location)
    ? comments.composer(comments.activeLocation) : null
}
