import { describe, expect, it } from 'vitest'
import { repositoryRoute } from './repositoryRoutes'

describe('repositoryRoute', () => {
  it('encodes project, path segments, and ref query values', () => {
    expect(repositoryRoute('holark', 'blob', 'docs/api guide#1.md', 'feature/test ref')).toBe(
      '/blob/docs/api%20guide%231.md?ref=feature%2Ftest%20ref',
    )
  })

  it('keeps the root tree route stable', () => {
    expect(repositoryRoute('holark', 'tree', '', 'main')).toBe('/tree/?ref=main')
  })
})
