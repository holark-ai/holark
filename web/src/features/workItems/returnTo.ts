export function listReturnTo(search: string, fallback: '/pulls' | '/issues') {
  const target = new URLSearchParams(search).get('returnTo') ?? ''
  return /^\/(my-work|pulls|issues)(\?[^#]*)?$/.test(target) ? target : fallback
}
