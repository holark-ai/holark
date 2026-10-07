import type { RepositoryBlobResponse, RepositoryCommit, RepositoryRefsResponse, RepositoryTreeEntry } from '../data/types'

// Cross several 50-commit page boundaries, including a day split between pages.
const commitsPerDay = [24, 16, 32, 12, 19, 10, 14, 40, 11, 17, 28, 35, 22, 31, 26]
const commitTopics = [
  'repository navigation', 'branch selection', 'file search', 'agent activity',
  'terminal sessions', 'pull request reviews', 'project settings', 'keyboard shortcuts',
  'workspace notifications', 'commit history',
]
const commitMessages = [
  'Simplify {topic}', 'Preserve focus in {topic}', 'Fix stale data in {topic}',
  'Improve keyboard access to {topic}', 'Handle empty results in {topic}',
  'Reduce extra requests for {topic}', 'Clarify loading feedback for {topic}',
  'Keep selection when refreshing {topic}', 'Support long names in {topic}',
  'Document {topic}', 'Improve error messages for {topic}', 'Tidy spacing in {topic}',
  'Cancel obsolete requests for {topic}', 'Restore scroll position in {topic}',
  'Improve contrast in {topic}', 'Handle reconnects in {topic}',
]
const commitAuthors = ['Riley', 'Ada', 'Navigation agent', 'Riley', 'Search agent']
// Main starts at index 1; include index 0 for the navigation branch as well.
const recentCommitBodies = [
  'Keep the workspace consistent while navigating between projects and refreshing repository data.',
  'Return focus to the selected file after closing the branch picker.',
  'Discard directory responses from the previous branch when a newer request has already completed. This keeps the file list and breadcrumbs aligned while switching quickly between repository snapshots.',
  'Let keyboard users move through the repository without losing their place.\n\nKeep the focused row visible when expanding a folder, and return focus to the trigger after dismissing a menu. The same behavior applies when navigating back from an opened file.',
  'Explain why a directory has no visible files and provide a clear route back to the repository root.\n\n- Distinguish an empty folder from a search with no matches.\n- Keep the selected branch and commit while clearing the search.\n- Show loading feedback until the new directory response arrives.\n\nThis also covers historical snapshots where a path has been renamed or removed.',
  'Reuse repository data while moving between the file browser and commit changes so that opening a familiar view does not repeatedly fetch the same snapshot. Requests remain tied to the resolved commit SHA, keeping historical results stable even when the branch advances.\n\nCombine overlapping refreshes and cancel work that belongs to an abandoned navigation. When switching branches quickly, only the most recent selection may update the visible files, breadcrumbs, and commit details.\n\nThe latest branch view still refreshes normally. Explicitly selected commits retain their cached contents, and a failed request can be retried without clearing the rest of the page.\n\nChecked directory navigation, returning from an opened file, selecting older commits, and moving between Files and Changes with a narrow viewport.',
]
const previewNow = new Date()

export const previewRepositoryCommits: RepositoryCommit[] = commitsPerDay.flatMap((count, dayIndex) => {
  const start = new Date(previewNow)
  start.setDate(start.getDate() - dayIndex)
  start.setHours(dayIndex === 0 ? 0 : 9, 0, 0, 0)
  const end = new Date(start)
  if (dayIndex === 0) end.setTime(previewNow.getTime())
  else end.setHours(18, 30, 0, 0)

  const offset = commitsPerDay.slice(0, dayIndex).reduce((total, amount) => total + amount, 0)
  return Array.from({ length: count }, (_, row) => {
    const index = offset + row
    const topic = commitTopics[(dayIndex + Math.floor(row / commitMessages.length)) % commitTopics.length]
    const message = commitMessages[row % commitMessages.length].replace('{topic}', topic)
    const body = recentCommitBodies[index] ?? (index % 9 === 0 ? 'Keep the workspace consistent while navigating between projects and refreshing repository data.' : '')
    // Stable, full-length sample hashes keep snapshot URLs valid across reloads.
    const sha = index === 0 ? '54ce287b8a923c5681df293aae9269ade3461890'
      : index === 1 ? '15ad9fe139a0492d57418d4a457658089acfbd12'
      : Array.from({ length: 5 }, (_, part) => ((Math.imul(index + 1, 0x9e3779b1) + Math.imul(part + 1, 0x85ebca6b)) >>> 0).toString(16).padStart(8, '0')).join('')
    return {
      sha,
      message: body ? message + '\n\n' + body : message,
      author_name: commitAuthors[index % commitAuthors.length],
      authored_at: new Date(end.getTime() - (end.getTime() - start.getTime()) * row / count).toISOString(),
    }
  })
})

export const previewRepositoryRefs: RepositoryRefsResponse = {
  default_ref: 'refs/heads/main',
  refs: [
    'main',
    'develop',
    'feature/repository-navigation',
    'feature/repository-search',
    'feature/repository-file-history',
    'feature/branch-comparison',
    'feature/dashboard-filters',
    'feature/dashboard-saved-views',
    'feature/keyboard-shortcuts',
    'feature/notifications',
    'feature/pull-request-review',
    'feature/workspace-navigation',
    'feature/workspace-settings',
    'feature/repository-navigation-preserve-selected-branch-across-directories',
    'fix/branch-picker-focus',
    'fix/branch-picker-toggle',
    'fix/dashboard-empty-state',
    'fix/search-result-ordering',
    'fix/terminal-resize',
    'chore/dependency-updates',
    'chore/remove-unused-assets',
    'docs/getting-started',
    'docs/repository-navigation',
    'release/1.0',
    'release/1.1',
    'release/2.0-beta',
    'hotfix/1.0.1',
    'experiment/compact-sidebar',
    'experiment/virtualized-file-list',
    'users/ada/review-comments',
    'users/riley/branch-picker-polish',
  ].map((branch, index) => {
    const commit = previewRepositoryCommits[branch === 'main' ? 1 : branch === 'feature/repository-navigation' ? 0 : index + 2]
    return {
      name: `refs/heads/${branch}`, short_name: branch, kind: 'branch',
      target: commit.sha,
      committed_at: commit.authored_at,
      author_name: commit.author_name,
      subject: commit.message.split('\n')[0],
    }
  }),
}

type PreviewFile = Pick<RepositoryBlobResponse, 'content' | 'language'>

const webReadme = `# Web interface

Repository browsing, Holon workspaces, and terminal views for **Acme Dashboard**.
The interface brings project files, active sessions, and code reviews into one workspace.

## Overview

Use the repository browser to explore files on any branch. Folder links and breadcrumbs
keep the selected branch as you move through the project. Open a source file to inspect
its contents, or start a Holon from the current directory.

> This is a development preview. The repository, branches, and file contents are sample data.

## Getting started

### Requirements

- Node.js and pnpm
- A modern browser
- A local checkout of the project

### Installation

1. Open the web directory.
2. Install dependencies.
3. Start the development server.


~~~sh
cd web
pnpm install
pnpm dev
~~~

Visit the local address printed by the development server. Changes to components and
styles appear as you edit them.

## Project structure

~~~text
web/
├── public/
├── src/
│   ├── components/
│   │   └── repository/
│   │       └── breadcrumbs/
│   ├── hooks/
│   ├── layouts/
│   ├── pages/
│   └── styles/
└── tests/
    ├── integration/
    └── fixtures/
        └── repository/
~~~

### Responsibilities

| Directory | Purpose | Example |
| --- | --- | --- |
| \`components\` | Reusable interface elements | Session tabs and breadcrumbs |
| \`hooks\` | Shared state and interactions | Active terminal selection |
| \`layouts\` | Page structure | Repository and workspace layouts |
| \`pages\` | Views opened through navigation | Files, branches, and commits |
| \`styles\` | Shared presentation | Colors, spacing, and workspace styles |

## Working with the repository

### Browse a branch

Choose a branch from the header, then open a folder. The branch picker supports filtering
by partial names such as \`repository\`, \`release\`, or \`fix/\`.

- Explore \`src/components/repository/breadcrumbs\` for a deeper path.
- Use the breadcrumbs to return to a parent folder.
- Switch branches while viewing a directory to keep your place.

### Review a change

1. Open a Holon workspace.
2. Expand the changes panel.
   - Select one commit to inspect its changes.
   - Select several commits to compare the combined result.
3. Open a changed file and review the diff.

## Components

Keep components small and give interactive elements clear labels. For example:

~~~tsx
export function SessionTabs({ sessions, active, onSelect }) {
  return (
    <nav aria-label="Sessions">
      {sessions.map(session => (
        <button
          key={session.id}
          aria-pressed={session.id === active}
          onClick={() => onSelect(session.id)}
        >
          {session.title}
        </button>
      ))}
    </nav>
  )
}
~~~

### Interaction checklist

- [x] Preserve the selected branch during navigation
- [x] Support keyboard navigation in the branch picker
- [x] Show a clear empty state when filtering has no matches
- [ ] Review narrow layouts with long paths
- [ ] Check light and dark themes

## Development commands

| Command | Description |
| --- | --- |
| \`pnpm dev\` | Start the local development server |
| \`pnpm build\` | Build the web interface |

## Troubleshooting

### A folder is missing on another branch

Branches can contain different files. Return to the repository root and browse from there,
or select the previous branch to return to its contents.

### A branch name is too long

Try \`feature/repository-navigation-preserve-selected-branch-across-directories\` in the
branch picker. This sample exercises truncation and filtering in a crowded list.

### Keyboard navigation

Use **Tab** to move between controls, **Enter** to activate them, and **Escape** to dismiss
an open menu. Focus indicators should remain visible without changing the width of text.

---

## Further reading

- [React documentation](https://react.dev/)
- [TypeScript documentation](https://www.typescriptlang.org/docs/)
- [Vite documentation](https://vite.dev/)

*Sample documentation for exploring Markdown rendering in the development preview.*
`

// Generate large browsing samples once, without checking in thousands of fixture lines.
const previewDependencies = Object.fromEntries(Array.from({ length: 5_000 }, (_, index) => [
  `@acme/workspace-module-${String(index + 1).padStart(5, '0')}`,
  `^${1 + index % 4}.${index % 20}.0`,
]))
const largePackageJson = `${JSON.stringify({
  name: 'acme-dashboard',
  private: true,
  scripts: { dev: 'vite', build: 'tsc && vite build' },
  dependencies: { react: '^19.0.0', 'react-dom': '^19.0.0', ...previewDependencies },
}, null, 2)}\n`
const largeLockfile = `lockfileVersion: '9.0'

settings:
  autoInstallPeers: true

importers:
  .:
    dependencies:
${Object.entries(previewDependencies).map(([name, version]) => `      '${name}':
        specifier: ${version}
        version: ${version.slice(1)}`).join('\n')}

packages:
${Object.entries(previewDependencies).map(([name, version]) => `  '${name}@${version.slice(1)}':
    resolution:
      tarball: https://registry.example.com/${name}/-/workspace-module-${version.slice(1)}.tgz
    engines: {node: '>=20'}
`).join('\n')}
snapshots:
${Object.entries(previewDependencies).map(([name, version]) => `  '${name}@${version.slice(1)}': {}`).join('\n')}
`

export function repositoryPreviewFiles(ref: string): Record<string, PreviewFile> {
  const webFiles: Record<string, PreviewFile> = {
    'README.md': { language: 'markdown', content: webReadme },
    '.gitignore': { language: 'plaintext', content: 'node_modules/\ndist/\n.env.local\n' },
    'index.html': { language: 'html', content: '<!doctype html>\n<html lang="en"><head><title>Acme Dashboard</title></head><body><div id="root"></div><script type="module" src="/src/main.tsx"></script></body></html>\n' },
    'package.json': { language: 'json', content: largePackageJson },
    'pnpm-lock.yaml': { language: 'yaml', content: largeLockfile },
    'tsconfig.json': { language: 'json', content: '{"compilerOptions":{"target":"ES2022","jsx":"react-jsx","strict":true}}\n' },
    'vite.config.ts': { language: 'typescript', content: "import { defineConfig } from 'vite'\nexport default defineConfig({})\n" },
    'src/main.tsx': { language: 'typescript', content: "import { createRoot } from 'react-dom/client'\ncreateRoot(document.getElementById('root')!).render(<h1>Acme Dashboard</h1>)\n" },
    'src/components/SessionTabs.tsx': { language: 'typescript', content: 'export function SessionTabs({ sessions, active, onSelect }) {\n  return (\n    <nav className="session-tabs" aria-label="Sessions">\n      {sessions.map(session => (\n        <button key={session.id} aria-pressed={session.id === active} onClick={() => onSelect(session.id)}>\n          {session.title}\n        </button>\n      ))}\n    </nav>\n  )\n}\n' },
    'src/components/repository/BranchPicker.tsx': { language: 'typescript', content: 'export function BranchPicker({ branches, value, onChange }) {\n  return (\n    <select aria-label="Branch" value={value} onChange={event => onChange(event.target.value)}>\n      {branches.map(branch => <option key={branch}>{branch}</option>)}\n    </select>\n  )\n}\n' },
    'src/components/repository/breadcrumbs/Breadcrumbs.tsx': { language: 'typescript', content: 'export function Breadcrumbs({ segments }) {\n  return (\n    <nav aria-label="Repository breadcrumb">\n      {segments.map(segment => <a key={segment.path} href={segment.href}>{segment.name}</a>)}\n    </nav>\n  )\n}\n' },
    'src/components/repository/breadcrumbs/Breadcrumbs.module.css': { language: 'css', content: '.breadcrumbs {\n  display: flex;\n  align-items: center;\n  gap: 8px;\n  flex-wrap: wrap;\n}\n' },
    'src/components/repository/breadcrumbs/README.md': { language: 'markdown', content: '# Repository breadcrumbs\n\nBreadcrumbs link each folder in the current path to its directory view.\n\n## Example\n\n`web / src / components / repository / breadcrumbs`\n\nKeep the selected branch in each link so navigating to a parent folder preserves context.\n' },
    'src/hooks/useActiveTerminal.ts': { language: 'typescript', content: "import { useState } from 'react'\n\nexport function useActiveTerminal(workspaceId: string) {\n  const key = `activeTerminal:${workspaceId}`\n  const [active, setActive] = useState(() => localStorage.getItem(key) ?? 'agent-1')\n\n  function selectTerminal(id: string) {\n    if (workspaceId) localStorage.setItem(key, id)\n    setActive(id)\n  }\n\n  return { active, selectTerminal }\n}\n" },
    'src/layouts/RepositoryLayout.tsx': { language: 'typescript', content: 'export function RepositoryLayout({ sidebar, children }) {\n  return <div className="repository-layout"><aside>{sidebar}</aside><main>{children}</main></div>\n}\n' },
    'src/layouts/WorkspaceLayout.tsx': { language: 'typescript', content: 'export function WorkspaceLayout({ header, children }) {\n  return <section className="workspace"><header>{header}</header><main>{children}</main></section>\n}\n' },
    'src/pages/RepositoryPage.tsx': { language: 'typescript', content: 'export function RepositoryPage({ files }) {\n  return <ul>{files.map(file => <li key={file.path}><a href={file.href}>{file.name}</a></li>)}</ul>\n}\n' },
    'src/pages/WorkspacePage.tsx': { language: 'typescript', content: 'export function WorkspacePage({ title, children }) {\n  return <section><h1>{title}</h1>{children}</section>\n}\n' },
    'src/styles/workspace.css': { language: 'css', content: '.session-tabs {\n  display: flex;\n  gap: 8px;\n}\n\n.session-tabs button {\n  border-radius: 999px;\n  padding: 6px 14px;\n}\n\n.session-tabs button[aria-pressed="true"] {\n  background: #eeedf5;\n}\n' },
    'public/robots.txt': { language: 'plaintext', content: 'User-agent: *\nAllow: /\n' },
    'public/workspace-preview.png': { language: 'plaintext', content: 'Binary image placeholder from the redesign mockup.\nThe dev repository preview displays text files only.\n' },
    'tests/README.md': { language: 'markdown', content: '# Tests\n\nIntegration coverage for the web interface.\n' },
    'tests/integration/repository/README.md': { language: 'markdown', content: '# Repository navigation scenarios\n\n1. Open a nested directory.\n2. Switch branches without losing the path.\n3. Follow a breadcrumb to a parent folder.\n4. Open a source file and return to its directory.\n' },
    'tests/fixtures/repository/branches.json': { language: 'json', content: '[\n  { "name": "main", "default": true },\n  { "name": "feature/repository-navigation", "default": false }\n]\n' },
  }
  const files: Record<string, PreviewFile> = {
    'README.md': { language: 'markdown', content: webReadme.replace('# Web interface', '# Acme Dashboard') },
    ...Object.fromEntries(Object.entries(webFiles).map(([path, file]) => [`web/${path}`, file])),
  }
  if (ref === 'refs/heads/feature/repository-navigation') {
    files['docs/repository-navigation.md'] = { language: 'markdown', content: '# Repository navigation\n\nKeep the selected branch in directory links, file links, and breadcrumbs.\n' }
    files['README.md'] = { ...files['README.md'], content: `${files['README.md'].content}\n## Repository navigation\n\nThis branch adds navigation notes in the docs directory.\n` }
  }
  return files
}

export function repositoryPreviewEntries(files: Record<string, PreviewFile>, path: string): RepositoryTreeEntry[] {
  const prefix = path ? `${path}/` : ''
  const entries = new Map<string, RepositoryTreeEntry>()
  for (const [filePath, file] of Object.entries(files)) {
    if (!filePath.startsWith(prefix)) continue
    const relative = filePath.slice(prefix.length)
    const name = relative.split('/')[0]
    const directory = relative.includes('/')
    entries.set(name, {
      name, path: `${prefix}${name}`, type: directory ? 'directory' : 'file', mode: directory ? '040000' : '100644',
      ...(directory ? {} : { size: new TextEncoder().encode(file.content).length, language: file.language }),
    })
  }
  return [...entries.values()].sort((a, b) => Number(b.type === 'directory') - Number(a.type === 'directory') || a.name.localeCompare(b.name))
}
