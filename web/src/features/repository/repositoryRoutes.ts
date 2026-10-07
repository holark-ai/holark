export function repositoryRoute(_repositoryId: string, mode: 'tree' | 'blob', path: string, refName: string, commit?: string) {
  const encodedPath = path.split('/').filter(Boolean).map(encodeURIComponent).join('/')
  const suffix = encodedPath ? `/${encodedPath}` : '/'
  return `/${mode}${suffix}?ref=${encodeURIComponent(refName)}${commit ? '&commit=' + encodeURIComponent(commit) : ''}`
}
