import { BrowserRouter } from 'react-router-dom'
import { AppRoutes } from './routes'
import { ColorThemeProvider } from './ColorThemeProvider'
import { ContextTokenThresholdsProvider } from './ContextTokenThresholdsProvider'
import { TerminalWorkspaceProvider } from '../features/terminal/TerminalWorkspaceProvider'

export function App() {
  const devPreview = devPreviewEnabled() && (window.location.pathname === '/__dev' || window.location.pathname.startsWith('/__dev/'))
  return (
    <ColorThemeProvider>
      <ContextTokenThresholdsProvider>
        <TerminalWorkspaceProvider>
          <BrowserRouter basename={devPreview ? "/__dev" : undefined}>
            <AppRoutes devPreview={devPreview} />
          </BrowserRouter>
        </TerminalWorkspaceProvider>
      </ContextTokenThresholdsProvider>
    </ColorThemeProvider>
  )
}

function devPreviewEnabled() {
  return import.meta.env.DEV || import.meta.env.VITE_HOLARK_TERMINAL_E2E === '1'
}
