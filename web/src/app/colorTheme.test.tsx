import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ColorThemeProvider } from './ColorThemeProvider'
import { colorThemeStorageKey, useColorTheme } from './colorTheme'

function ThemeProbe() {
  const { theme, setTheme } = useColorTheme()
  return (
    <div>
      <output aria-label="Active theme">{theme}</output>
      <button type="button" onClick={() => setTheme('dark')}>Use dark</button>
    </div>
  )
}

function renderThemeProbe() {
  return render(<ColorThemeProvider><ThemeProbe /></ColorThemeProvider>)
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  document.documentElement.removeAttribute('style')
  let meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
  if (!meta) {
    meta = document.createElement('meta')
    meta.name = 'theme-color'
    document.head.append(meta)
  }
  meta.content = ''
})

afterEach(() => {
  vi.restoreAllMocks()
})

it('defaults to light and persists an immediately applied dark preference', async () => {
  const user = userEvent.setup()
  renderThemeProbe()

  expect(screen.getByLabelText('Active theme')).toHaveTextContent('light')
  expect(document.documentElement).toHaveAttribute('data-theme', 'light')
  expect(document.documentElement.style.colorScheme).toBe('light')
  expect(document.querySelector('meta[name="theme-color"]')).toHaveAttribute('content', '#f5f4f0')

  await user.click(screen.getByRole('button', { name: 'Use dark' }))

  expect(screen.getByLabelText('Active theme')).toHaveTextContent('dark')
  expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
  expect(document.documentElement.style.colorScheme).toBe('dark')
  expect(document.querySelector('meta[name="theme-color"]')).toHaveAttribute('content', '#181917')
  expect(window.localStorage.getItem(colorThemeStorageKey)).toBe('dark')
})

it('restores a stored preference and follows changes from another tab', () => {
  window.localStorage.setItem(colorThemeStorageKey, 'light')
  renderThemeProbe()

  expect(screen.getByLabelText('Active theme')).toHaveTextContent('light')
  expect(document.documentElement).toHaveAttribute('data-theme', 'light')

  act(() => {
    window.dispatchEvent(new StorageEvent('storage', {
      key: colorThemeStorageKey,
      newValue: 'dark',
      storageArea: window.localStorage,
    }))
  })

  expect(screen.getByLabelText('Active theme')).toHaveTextContent('dark')
  expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
  expect(document.querySelector('meta[name="theme-color"]')).toHaveAttribute('content', '#181917')
})

it('safely falls back to light for invalid or inaccessible storage', () => {
  window.localStorage.setItem(colorThemeStorageKey, 'sepia')
  const invalid = renderThemeProbe()

  expect(screen.getByLabelText('Active theme')).toHaveTextContent('light')
  invalid.unmount()

  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
    throw new DOMException('Storage is blocked', 'SecurityError')
  })
  renderThemeProbe()

  expect(screen.getByLabelText('Active theme')).toHaveTextContent('light')
  expect(document.documentElement).toHaveAttribute('data-theme', 'light')
})
