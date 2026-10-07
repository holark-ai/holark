import { createContext, useContext, useEffect } from 'react'
import type { ProjectMember } from '../../data/types'

export type ProjectMemberState =
  | { status: 'loading'; member?: undefined }
  | { status: 'missing'; member?: undefined }
  | { status: 'unavailable'; member?: undefined }
  | { status: 'resolved'; member: ProjectMember }

export type ProjectMemberStoreValue = {
  revision: number
  getMemberState: (memberId: string) => ProjectMemberState
  requestMembers: (memberIds: string[]) => void
  searchMembers: (query: string, limit?: number) => Promise<ProjectMember[]>
}

export const ProjectMemberContext = createContext<ProjectMemberStoreValue | null>(null)

export function useProjectMemberStore() {
  const store = useContext(ProjectMemberContext)
  if (!store) throw new Error('ProjectMemberContext is missing')
  return store
}

export function useProjectMember(memberId: string) {
  const store = useProjectMemberStore()
  const { getMemberState, requestMembers } = store
  const normalizedID = memberId.trim()

  useEffect(() => {
    if (normalizedID) requestMembers([normalizedID])
  }, [normalizedID, requestMembers])

  return normalizedID ? getMemberState(normalizedID) : { status: 'missing' as const }
}
