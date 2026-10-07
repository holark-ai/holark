import { createContext, useContext } from 'react'

export type ColorTheme = 'dark' | 'light'

export const colorThemeStorageKey = 'holark.color-theme.shared-tree'

const themeColors: Record<ColorTheme, string> = {
  dark: '#181917',
  light: '#f5f4f0',
}

type ColorThemeContextValue = {
  theme: ColorTheme
  setTheme: (theme: ColorTheme) => void
}

export const ColorThemeContext = createContext<ColorThemeContextValue>({
  theme: 'light',
  setTheme: () => undefined,
})

export function isColorTheme(value: unknown): value is ColorTheme {
  return value === 'dark' || value === 'light'
}

export function readStoredColorTheme(): ColorTheme {
  try {
    const stored = window.localStorage.getItem(colorThemeStorageKey)
    return isColorTheme(stored) ? stored : 'light'
  } catch {
    return 'light'
  }
}

export function applyColorTheme(theme: ColorTheme) {
  const root = document.documentElement
  root.dataset.theme = theme
  root.style.colorScheme = theme
  document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')?.setAttribute('content', themeColors[theme])
}

export function initializeColorTheme() {
  const theme = readStoredColorTheme()
  applyColorTheme(theme)
  return theme
}


export function useColorTheme() {
  return useContext(ColorThemeContext)
}

