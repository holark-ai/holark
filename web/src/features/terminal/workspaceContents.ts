import { api } from '../../data/api'
import type { WorkspaceFileDiff } from '../../data/types'

// One pool serves a mounted holon across range changes. Aborted queued work never starts, and
// an in-flight slot remains occupied until its fetch has actually settled.
export function createContentLoader() {
  let active = 0
  const pending: Array<() => void> = []
  const pump = () => { while (active < 2 && pending.length) pending.shift()!() }
  return function load(holonId: string, base: string, target: string, path: string, signal: AbortSignal) {
    return new Promise<WorkspaceFileDiff>((resolve, reject) => {
      pending.push(() => {
        if (signal.aborted) { reject(new DOMException('Aborted', 'AbortError')); return }
        active += 1
        api.sessionChanges(holonId, { base, target, path, contents: true, signal }).then((result) => {
          const file = result.files.find((item) => item.path === path)
          if (!file) throw new Error('This file no longer differs in this range.')
          resolve(file)
        }).catch(reject).finally(() => { active -= 1; pump() })
      })
      pump()
    })
  }
}

export type ContentLoader = ReturnType<typeof createContentLoader>

