import type { ITheme } from '@xterm/xterm'

type TerminalTypography = {
  fontFamily: string
  fontSize: number
  lineHeight: number
}

const vscodeDarkTheme: ITheme = {
  background: '#191A1B',
  foreground: '#CCCCCC',
  cursor: '#BFBFBF',
  cursorAccent: '#191A1B',
  selectionBackground: '#3994BC33',
  selectionInactiveBackground: '#3994BC1A',
  black: '#000000',
  red: '#CD3131',
  green: '#0DBC79',
  yellow: '#E5E510',
  blue: '#2472C8',
  magenta: '#BC3FBC',
  cyan: '#11A8CD',
  white: '#E5E5E5',
  brightBlack: '#666666',
  brightRed: '#F14C4C',
  brightGreen: '#23D18B',
  brightYellow: '#F5F543',
  brightBlue: '#3B8EEA',
  brightMagenta: '#D670D6',
  brightCyan: '#29B8DB',
  brightWhite: '#E5E5E5',
  scrollbarSliderBackground: '#A8A9AA55',
  scrollbarSliderHoverBackground: '#A8A9AA88',
  scrollbarSliderActiveBackground: '#A8A9AAB0',
}

const vscodeLightTheme: ITheme = {
  background: '#FFFFFF',
  foreground: '#333333',
  cursor: '#202020',
  cursorAccent: '#FFFFFF',
  selectionBackground: '#0069CC26',
  selectionInactiveBackground: '#0069CC13',
  black: '#000000',
  red: '#CD3131',
  green: '#107C10',
  yellow: '#949800',
  blue: '#0451A5',
  magenta: '#BC05BC',
  cyan: '#0598BC',
  white: '#555555',
  brightBlack: '#666666',
  brightRed: '#CD3131',
  brightGreen: '#14CE14',
  brightYellow: '#B5BA00',
  brightBlue: '#0451A5',
  brightMagenta: '#BC05BC',
  brightCyan: '#0598BC',
  brightWhite: '#A5A5A5',
  scrollbarSliderBackground: '#64646455',
  scrollbarSliderHoverBackground: '#64646488',
  scrollbarSliderActiveBackground: '#646464B0',
}

export function terminalTypographyForClient(): TerminalTypography {
  const platform = `${window.navigator.platform} ${window.navigator.userAgent}`
  if (/Mac|iPhone|iPad|iPod/i.test(platform)) {
    return { fontFamily: "Menlo, Monaco, 'Courier New', monospace", fontSize: 12, lineHeight: 1 }
  }
  if (/Win/i.test(platform)) {
    return { fontFamily: "Consolas, 'Courier New', monospace", fontSize: 14, lineHeight: 1 }
  }
  return { fontFamily: "'Droid Sans Mono', monospace", fontSize: 14, lineHeight: 1.1 }
}

export function terminalThemeFromTokens(colorTheme?: string): ITheme {
  const resolvedTheme = colorTheme ?? document.documentElement.dataset.theme
  return { ...(resolvedTheme === 'light' ? vscodeLightTheme : vscodeDarkTheme) }
}
