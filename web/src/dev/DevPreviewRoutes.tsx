import { lazy, Suspense } from 'react'
import { Route, Routes } from 'react-router-dom'
import { AppShell } from '../app/AppShell'
import { NotFound } from '../app/NotFound'
import { HolonPagePreview } from './HolonPagePreview'
import { RepositoryHistoryPreview } from './RepositoryHistoryPreview'
import { RepositoryView } from '../features/repository/RepositoryView'

const IssueDetail = lazy(() => import('../features/issues/IssueViews').then((module) => ({ default: module.IssueDetail })))
const IssueList = lazy(() => import('../features/issues/IssueViews').then((module) => ({ default: module.IssueList })))
const MyWork = lazy(() => import('../features/workItems/WorkItems').then((module) => ({ default: module.MyWork })))

const PullRequestList = lazy(() => import('../features/pullRequests/PullRequestViews').then((module) => ({ default: module.PullRequestList })))
const PullRequestDetail = lazy(() => import('../features/pullRequests/PullRequestViews').then((module) => ({ default: module.PullRequestDetail })))

export function DevPreviewRoutes() {
  return (
    <Routes>
      <Route path="/" element={<AppShell />}>
        <Route index element={<RepositoryHistoryPreview />} />
        <Route path="tree/*" element={<RepositoryHistoryPreview />} />
        <Route path="blob/*" element={<RepositoryView mode="blob" />} />
        <Route path="holon" element={<HolonPagePreview />} />
        <Route path="my-work" element={<Suspense fallback={<div>Loading My work…</div>}><MyWork /></Suspense>} />
        <Route path="holons/:holonId" element={<HolonPagePreview />} />
        <Route path="issues" element={<Suspense fallback={<div>Loading issues…</div>}><IssueList /></Suspense>} />
        <Route path="issues/:issueId" element={<Suspense fallback={<div>Loading issue…</div>}><IssueDetail /></Suspense>} />
        <Route path="pulls" element={<Suspense fallback={<div>Loading pull requests…</div>}><PullRequestList /></Suspense>} />
        <Route path="pulls/:pullRequestId" element={<Suspense fallback={<div>Loading pull request…</div>}><PullRequestDetail /></Suspense>} />
      </Route>
      <Route path="*" element={<NotFound />} />
    </Routes>
  )
}
