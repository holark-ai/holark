import { useCallback, useEffect, useLayoutEffect, useMemo, useState, type ReactNode } from 'react'
import { applyColorTheme, ColorThemeContext, colorThemeStorageKey, isColorTheme, readStoredColorTheme, type ColorTheme } from './colorTheme'

export function ColorThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<ColorTheme>(readStoredColorTheme)

  const setTheme = useCallback((nextTheme: ColorTheme) => {
    applyColorTheme(nextTheme)
    setThemeState(nextTheme)
    try {
      window.localStorage.setItem(colorThemeStorageKey, nextTheme)
    } catch {
      // The preference still applies for this page when browser storage is unavailable.
    }
  }, [])

  useLayoutEffect(() => {
    applyColorTheme(theme)
  }, [theme])

  useEffect(() => {
    const synchronizeTheme = (event: StorageEvent) => {
      if (event.key !== colorThemeStorageKey) return
      const nextTheme = isColorTheme(event.newValue) ? event.newValue : 'light'
      applyColorTheme(nextTheme)
      setThemeState(nextTheme)
    }
    window.addEventListener('storage', synchronizeTheme)
    return () => window.removeEventListener('storage', synchronizeTheme)
  }, [])

  const value = useMemo(() => ({ theme, setTheme }), [setTheme, theme])
  return <ColorThemeContext.Provider value={value}>{children}</ColorThemeContext.Provider>
}
