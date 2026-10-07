import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './app/App'
import { initializeColorTheme } from './app/colorTheme'
import { LaunchError } from './app/LaunchError'
import './styles/global.css'

async function bootstrap() {
  const launch = await window.__HOLARK_LAUNCH_READY__
  if ((import.meta.env.DEV || import.meta.env.VITE_HOLARK_TERMINAL_E2E === '1')
    && (window.location.pathname === '/__dev' || window.location.pathname.startsWith('/__dev/'))) {
    const { installDevPreviewApi } = await import('./dev/devPreviewApi')
    installDevPreviewApi()
  }
  initializeColorTheme()

  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      {launch?.status === 'error' ? <LaunchError reason={launch.reason} /> : <App />}
    </StrictMode>,
  )
}

void bootstrap()
