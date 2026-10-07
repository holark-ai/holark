import { createContext, useContext } from 'react'
import type { ApiProject } from '../../data/types'

export const ProjectContext = createContext<ApiProject | null>(null)

export function useProject() {
  const project = useContext(ProjectContext)
  if (!project) throw new Error('ProjectContext is missing')
  return project
}
