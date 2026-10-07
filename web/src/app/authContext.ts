import { createContext, useContext } from 'react'
import type { CurrentUser } from '../data/types'

export type AuthContextValue = {
  user: CurrentUser | null
}

export const AuthContext = createContext<AuthContextValue>({ user: null })

export function useAuth() {
  return useContext(AuthContext)
}
