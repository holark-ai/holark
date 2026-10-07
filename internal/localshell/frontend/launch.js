window.__HOLARK_LAUNCH_READY__ = (async () => {
  const secret = new URLSearchParams(window.location.search).get('launch')
  if (secret === null) return { status: 'ready' }

  try {
    const response = await fetch('/api/launch-session', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ secret }),
    })
    if (!response.ok) {
      return { status: 'error', reason: response.status === 401 ? 'invalid-token' : 'connection' }
    }

    window.history.replaceState({}, '', window.location.pathname)
    window.location.reload()
    await new Promise(() => {})
  } catch {
    return { status: 'error', reason: 'connection' }
  }
})()
