import type { Holon } from '../../data/types'

export function defaultPullRequestTitle(holon?: Holon) {
  return holon?.title || 'Agent changes'
}
