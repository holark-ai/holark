import { AgentCapabilitiesProvider } from '../data/AgentCapabilitiesProvider'
import { useCallback } from 'react'
import { ScrollArea } from '@base-ui/react/scroll-area'
import { Outlet } from 'react-router-dom'
import { NavigationRail } from '../features/navigation/NavigationRail'
import { OperationsPanel } from '../features/operations/OperationsPanel'
import { OperationsProvider } from '../features/operations/OperationsProvider'
import { NotFound } from './NotFound'
import { api } from '../data/api'
import { usePolling } from '../data/usePolling'
import { PageCacheProvider } from '../data/PageCacheProvider'
import { ProjectMemberStoreProvider } from '../features/members/ProjectMemberStore'
import { ProjectContext } from '../features/project/ProjectContext'
import { HolonStoreProvider } from '../features/project/HolonStore'
import { GitHubProfileProvider } from '../features/profile/GitHubProfileProvider'
import styles from './AppShell.module.css'

export function AppShell() {
  const loadProject = useCallback(() => api.project('repository'), [])
  const { data: project, error, loading } = usePolling(loadProject)

  if (loading) return <div className={styles.message}>Loading repository…</div>
  if (!project) return error ? <NotFound /> : <div className={styles.message}>Loading repository…</div>

  return (
    <AgentCapabilitiesProvider>
      <ProjectContext.Provider value={project}>
        <GitHubProfileProvider>
          <PageCacheProvider key={project.id}>
            <ProjectMemberStoreProvider key={project.id} projectId={project.id}>
              <HolonStoreProvider key={project.id} projectId={project.id}>
                <OperationsProvider projectId={project.id}>
                  <div className={styles.shell}>
                    <NavigationRail projectId={project.id} />
                    <ScrollArea.Root className={styles.main}>
                      <ScrollArea.Viewport className={styles.viewport} render={<main />} role="main">
                        <Outlet />
                      </ScrollArea.Viewport>
                      <ScrollArea.Scrollbar className={styles.scrollbar}>
                        <ScrollArea.Thumb data-overlay-scrollbar-thumb className={styles.thumb} />
                      </ScrollArea.Scrollbar>
                      <ScrollArea.Scrollbar orientation="horizontal" className={styles.scrollbar}>
                        <ScrollArea.Thumb data-overlay-scrollbar-thumb className={styles.thumb} />
                      </ScrollArea.Scrollbar>
                    </ScrollArea.Root>
                    <OperationsPanel />
                  </div>
                </OperationsProvider>
              </HolonStoreProvider>
            </ProjectMemberStoreProvider>
          </PageCacheProvider>
        </GitHubProfileProvider>
      </ProjectContext.Provider>
    </AgentCapabilitiesProvider>
  )
}
