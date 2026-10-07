import type { RepositoryBlobResponse } from '../data/types'

type PreviewFile = Pick<RepositoryBlobResponse, 'content' | 'language'>

// Each revision introduces a different feature, from oldest to newest.
// Include index 0 as well as the five commits visible on main (indices 1–5).
const features = [
  { name: 'navigation', title: 'Repository navigation', action: 'Open directory', count: 14 },
  { name: 'focus', title: 'Focus restoration', action: 'Restore focus', count: 18 },
  { name: 'requests', title: 'Request coordination', action: 'Cancel stale request', count: 22 },
  { name: 'keyboard', title: 'Keyboard navigation', action: 'Move selection', count: 16 },
  { name: 'empty-states', title: 'Empty directory feedback', action: 'Return to repository', count: 12 },
  { name: 'cache', title: 'Snapshot caching', action: 'Reuse snapshot', count: 24 },
]

const contexts = ['root', 'directory', 'file', 'search', 'branch', 'commit']

export function applyPreviewCommitChanges(files: Record<string, PreviewFile>, revision: number) {
  for (let index = features.length - 1; index >= 0; index--) {
    const feature = features[index]
    const applied = revision <= index
    const rules = Array.from({ length: feature.count }, (_, rule) => ({
      id: `${feature.name}-${rule + 1}`,
      context: contexts[rule % contexts.length],
      action: feature.action,
      preserveBranch: applied,
      preserveCommit: applied,
      retryLimit: applied ? 3 : 0,
      timeoutMs: applied ? 5_000 + rule * 250 : 30_000,
    }))
    // Modifications with ample unchanged context between changed lines.
    files[`web/src/repository/policies/${feature.name}.json`] = {
      language: 'json',
      content: JSON.stringify({ feature: feature.title, enabled: applied, rules }, null, 2) + '\n',
    }
    if (!applied) {
      files[`web/src/repository/legacy/${feature.name}.ts`] = {
        language: 'typescript',
        content: `// Legacy ${feature.title.toLowerCase()} defaults\n` + rules.map((rule, row) =>
          `export function legacyRule${row + 1}() {\n  return { context: '${rule.context}', timeout: 30000, preserveSelection: false }\n}\n`,
        ).join('\n'),
      }
      continue
    }
    // New source, styles, and documentation accompany each policy update.
    files[`web/src/repository/${feature.name}.ts`] = {
      language: 'typescript',
      content: `import policy from './policies/${feature.name}.json'

export type RepositoryContext = {
  path: string
  branch: string
  commit?: string
  signal: AbortSignal
}

export type NavigationResult = {
  href: string
  label: string
  preserveSelection: boolean
}

export function resolveActions(context: RepositoryContext): NavigationResult[] {
  if (context.signal.aborted) return []
  return policy.rules.map(rule => ({
    href: buildHref(context, rule.context),
    label: rule.action,
    preserveSelection: rule.preserveBranch && rule.preserveCommit,
  }))
}

function buildHref(context: RepositoryContext, view: string) {
  const params = new URLSearchParams({ ref: context.branch, view })
  if (context.commit) params.set('commit', context.commit)
  return '/repository/' + context.path + '?' + params.toString()
}

export const feedback = {
  pending: '${feature.title} is loading. Keep your current selection while the repository snapshot is resolved.',
  empty: 'No matching entries were found. Try another directory or clear the active filter to see every file in this snapshot.',
  failed: 'The repository could not be loaded. Your branch, commit, and current path have been preserved so you can safely retry this operation.',
}

export function describeSelection(context: RepositoryContext) {
  return [context.branch, context.commit?.slice(0, 7), context.path || '/']
    .filter(Boolean)
    .join(' · ')
}
`,
    }
    files[`web/src/repository/${feature.name}.module.css`] = {
      language: 'css',
      content: ['panel', 'toolbar', 'heading', 'description', 'actions', 'status', 'empty', 'retry'].map((name, row) =>
        `.${name} {\n  display: ${row % 2 ? 'flex' : 'grid'};\n  gap: ${4 + row * 2}px;\n  min-width: 0;\n  padding: ${8 + row}px;\n  overflow-wrap: anywhere;\n}\n`,
      ).join('\n') + '\n@media (max-width: 720px) {\n  .toolbar { flex-wrap: wrap; }\n  .actions { width: 100%; }\n}\n',
    }
    files[`docs/repository/${feature.name}.md`] = {
      language: 'markdown',
      content: `# ${feature.title}\n\n${feature.action} while keeping the current repository snapshot and branch visible.\n\n` +
        contexts.map(context => `## ${context[0].toUpperCase() + context.slice(1)} view

1. Open the ${context} view from the repository browser.
2. Select a branch and an older commit from the timeline.
3. Use “${feature.action}” and confirm the current selection stays visible.
4. Return to the latest snapshot and repeat with a narrow window.

The ${context} view keeps long paths readable by wrapping them within the available width. A pending request leaves the previous content visible until the next snapshot is ready.

### Recovery

If loading fails, keep the selected branch and commit, show the error beside the affected content, and offer a retry without resetting the rest of the repository view.
`).join('\n'),
    }
  }
}
