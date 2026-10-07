import { lazy, Suspense } from 'react'
import { Route, Routes } from 'react-router-dom'
import { AppShell } from './AppShell'
import { RepositoryView } from '../features/repository/RepositoryView'
import { NotFound } from './NotFound'

const HolonTerminal = lazy(() => import('../features/terminal/HolonTerminal').then((module) => ({ default: module.HolonTerminal })))
const PullRequestList = lazy(() => import('../features/workItems/WorkItems').then((module) => ({ default: module.PullRequestList })))
const MyWork = lazy(() => import('../features/workItems/WorkItems').then((module) => ({ default: module.MyWork })))
const PullRequestDetail = lazy(() => import('../features/pullRequests/PullRequestViews').then((module) => ({ default: module.PullRequestDetail })))
const IssueList = lazy(() => import('../features/issues/IssueViews').then((module) => ({ default: module.IssueList })))
const IssueDetail = lazy(() => import('../features/issues/IssueViews').then((module) => ({ default: module.IssueDetail })))
const SettingsView = lazy(() => import('../features/settings/SettingsView').then((module) => ({ default: module.SettingsView })))
const ProfilePage = lazy(() => import('../features/profile/ProfilePage').then((module) => ({ default: module.ProfilePage })))
const DevPreviewRoutes = (import.meta.env.DEV || import.meta.env.VITE_HOLARK_TERMINAL_E2E === '1')
  ? lazy(() => import('../dev/DevPreviewRoutes').then((module) => ({ default: module.DevPreviewRoutes })))
  : undefined

export function AppRoutes({ devPreview = false }: { devPreview?: boolean }) {
  if (devPreview && DevPreviewRoutes) {
    return <Suspense fallback={<div>Loading Holark preview…</div>}><DevPreviewRoutes /></Suspense>
  }
  return (
      <Routes>
        <Route path="/" element={<AppShell />}>
          <Route index element={<RepositoryView mode="tree" />} />
          <Route path="tree/*" element={<RepositoryView mode="tree" />} />
          <Route path="blob/*" element={<RepositoryView mode="blob" />} />
          <Route path="holons/:holonId" element={<Suspense fallback={<div>Loading terminal…</div>}><HolonTerminal /></Suspense>} />
          <Route path="my-work" element={<Suspense fallback={<div>Loading My work…</div>}><MyWork /></Suspense>} />
          <Route path="pulls" element={<Suspense fallback={<div>Loading pull requests…</div>}><PullRequestList /></Suspense>} />
          <Route path="pulls/:pullRequestId" element={<Suspense fallback={<div>Loading pull request…</div>}><PullRequestDetail /></Suspense>} />
          <Route path="issues" element={<Suspense fallback={<div>Loading issues...</div>}><IssueList /></Suspense>} />
          <Route path="issues/:issueId" element={<Suspense fallback={<div>Loading issue...</div>}><IssueDetail /></Suspense>} />
          <Route path="settings" element={<Suspense fallback={<div>Loading settings...</div>}><SettingsView /></Suspense>} />
          <Route path="profile" element={<Suspense fallback={<div>Loading profile…</div>}><ProfilePage /></Suspense>} />
        </Route>
        <Route path="*" element={<NotFound />} />
      </Routes>
  )
}
