type FullscreenOptionsWithKeyboardLock = FullscreenOptions & {
  keyboardLock: 'browser'
}

type KeyboardLockNavigator = Navigator & {
  keyboard?: {
    lock?: (keys?: string[]) => Promise<void>
    unlock?: () => void
  }
}

export function requestHolarkFullscreen() {
  if (document.fullscreenElement || !document.documentElement.requestFullscreen) return
  try {
    const request = document.documentElement.requestFullscreen({
      keyboardLock: 'browser',
    } as FullscreenOptionsWithKeyboardLock)
    void request.then(lockEscapeForLegacyBrowsers).catch(() => undefined)
  } catch {
    // Fullscreen is optional and must not block the action that requested it.
  }
}

export function exitHolarkFullscreen() {
  if (!document.fullscreenElement || !document.exitFullscreen) return
  try {
    void document.exitFullscreen().catch(() => undefined)
  } catch {
    // The browser can end fullscreen independently of Holark.
  }
}

export function releaseLegacyKeyboardLock() {
  try {
    keyboardLockApi()?.unlock?.()
  } catch {
    // Keyboard lock support varies by browser.
  }
}

function lockEscapeForLegacyBrowsers() {
  try {
    const request = keyboardLockApi()?.lock?.(['Escape'])
    void request?.catch(() => undefined)
  } catch {
    // The fullscreen keyboardLock option may already provide the lock.
  }
}

function keyboardLockApi() {
  return (navigator as KeyboardLockNavigator).keyboard
}
