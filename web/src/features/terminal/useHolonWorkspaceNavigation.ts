import { useState } from 'react'

export function useHolonWorkspaceNavigation(onSelectTab: (id: string) => void) {
  const [diffOpen, setDiffOpen] = useState(false)

  const selectTab = (id: string) => {
    setDiffOpen(false)
    onSelectTab(id)
  }

  return { diffOpen, setDiffOpen, selectTab }
}
