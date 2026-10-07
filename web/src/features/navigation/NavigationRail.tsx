import { BarChart3, CircleDollarSign, ListTodo, CircleAlert, GitPullRequest, LogOut, Maximize2, Minimize2, Moon, Sun, Settings, Folder, UserRound } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import type { CurrentUser } from '../../data/types'
import { useColorTheme } from '../../app/colorTheme'
import { useAuth } from '../../app/authContext'
import { exitHolarkFullscreen, releaseLegacyKeyboardLock, requestHolarkFullscreen } from '../fullscreen/fullscreen'
import holarkLogo from '../../assets/holark-logo-small.png'
import styles from './NavigationRail.module.css'

const fullscreenHintStorageKey = 'holark.fullscreen-hint-shown'
const fullscreenHintDelayMs = 2_000
const fullscreenAnimationDurationMs = 1_200
const fullscreenTooltipDurationMs = 5_000

export function NavigationRail({ projectId }: { projectId: string }) {
  void projectId
  const location = useLocation()
  const { pathname } = location
  const { user } = useAuth()
  const { theme, setTheme } = useColorTheme()
  const themeLabel = theme === 'light' ? 'Switch to dark theme' : 'Switch to light theme'
  const [accountOpen, setAccountOpen] = useState(false)
  const [fullscreen, setFullscreen] = useState(() => Boolean(document.fullscreenElement))
  const [fullscreenTooltipHint, setFullscreenTooltipHint] = useState(false)
  const fullscreenButtonRef = useRef<HTMLButtonElement>(null)
  const projectPath = ""
  const repositoryActive = pathname === '/' || pathname.startsWith(`${projectPath}/tree`) || pathname.startsWith(`${projectPath}/blob`)
  const pullRequestsActive = pathname === `${projectPath}/pulls` || pathname.startsWith(`${projectPath}/pulls/`)
  const issuesActive = pathname === `${projectPath}/issues` || pathname.startsWith(`${projectPath}/issues/`)
  const settingsActive = pathname === `${projectPath}/settings`
  const accountLabel = user ? userLabel(user) : ''

  useEffect(() => {
    const syncFullscreen = () => {
      const active = Boolean(document.fullscreenElement)
      setFullscreen(active)
      if (active) setFullscreenTooltipHint(false)
      if (!active) releaseLegacyKeyboardLock()
    }
    const preserveFullscreenOnEscape = (event: KeyboardEvent) => {
      if (document.fullscreenElement && event.key === 'Escape') event.preventDefault()
    }
    document.addEventListener('fullscreenchange', syncFullscreen)
    document.addEventListener('keydown', preserveFullscreenOnEscape, true)
    return () => {
      document.removeEventListener('fullscreenchange', syncFullscreen)
      document.removeEventListener('keydown', preserveFullscreenOnEscape, true)
    }
  }, [])

  useEffect(() => {
    if (!fullscreenTooltipHint) return
    const timeout = window.setTimeout(() => setFullscreenTooltipHint(false), fullscreenTooltipDurationMs)
    return () => window.clearTimeout(timeout)
  }, [fullscreenTooltipHint])

  useEffect(() => {
    if (!location.state?.suggestFullscreen || fullscreen) return
    const button = fullscreenButtonRef.current
    if (!button) return

    const showTooltip = !fullscreenHintHasBeenShown()
    let clearAnimationTimeout: number | undefined
    const clearAnimation = () => button.removeAttribute('data-fullscreen-hint')
    const showHintTimeout = window.setTimeout(() => {
      button.dataset.fullscreenHint = 'true'
      clearAnimationTimeout = window.setTimeout(clearAnimation, fullscreenAnimationDurationMs)
      if (showTooltip) {
        markFullscreenHintShown()
        setFullscreenTooltipHint(true)
      }
    }, fullscreenHintDelayMs)

    return () => {
      window.clearTimeout(showHintTimeout)
      if (clearAnimationTimeout !== undefined) window.clearTimeout(clearAnimationTimeout)
      clearAnimation()
    }
  }, [fullscreen, location.key, location.state])

  return (
    <nav className={styles.rail} aria-label="Primary navigation">
      <Link className={styles.mark} to={"/"} aria-label="Holark home" data-tooltip="Holark">
        <img className={styles.brandLogo} src={holarkLogo} alt="" />
      </Link>
      <div className={styles.items}>
        <Link className={itemClass(repositoryActive)} to={"/"} aria-label="Repository" aria-current={repositoryActive ? 'page' : undefined} data-tooltip="Repository">
          <Folder />
        </Link>
        <Link className={itemClass(pathname === "/my-work")} to="/my-work" aria-label="My work" aria-current={pathname === "/my-work" ? "page" : undefined} data-tooltip="My work"><ListTodo /></Link>
        <Link className={itemClass(pullRequestsActive)} to={"/pulls"} aria-label="Pull requests" aria-current={pullRequestsActive ? 'page' : undefined} data-tooltip="Pull requests">
          <GitPullRequest />
        </Link>
        <Link className={itemClass(issuesActive)} to={"/issues"} aria-label="Issues" aria-current={issuesActive ? 'page' : undefined} data-tooltip="Issues">
          <CircleAlert />
        </Link>
        <Link className={itemClass(settingsActive)} to={"/settings"} aria-label="Settings" aria-current={settingsActive ? 'page' : undefined} data-tooltip="Settings">
          <Settings />
        </Link>
        <Link className={itemClass(pathname === '/profile')} to="/profile" aria-label="Profile" aria-current={pathname === '/profile' ? 'page' : undefined} data-tooltip="Profile">
          <UserRound />
        </Link>
      </div>
      <button
        className={`${styles.item} ${styles.themeButton}`}
        type="button"
        aria-label={themeLabel}
        data-tooltip={themeLabel}
        onClick={() => setTheme(theme === 'light' ? 'dark' : 'light')}
      >
        {theme === 'light' ? <Moon aria-hidden="true" /> : <Sun aria-hidden="true" />}
      </button>
      <button
        ref={fullscreenButtonRef}
        className={`${styles.item} ${styles.fullscreenButton}`}
        type="button"
        aria-label={fullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
        aria-pressed={fullscreen}
        data-tooltip={fullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
        data-fullscreen-tooltip-hint={fullscreenTooltipHint || undefined}
        onMouseLeave={() => setFullscreenTooltipHint(false)}
        onClick={fullscreen ? exitHolarkFullscreen : requestHolarkFullscreen}
      >
        {fullscreen ? <Minimize2 /> : <Maximize2 />}
      </button>
      {user ? (
        <div className={styles.account}>
          <button
            className={styles.accountButton}
            type="button"
            aria-label={`Signed in as ${accountLabel}`}
            aria-expanded={accountOpen}
            data-tooltip={accountLabel}
            onClick={() => setAccountOpen((open) => !open)}
          >
            <span aria-hidden="true">{userInitials(user)}</span>
          </button>
          {accountOpen ? (
            <div className={styles.accountMenu} role="menu">
              <div className={styles.accountIdentity}>
                <div className={styles.accountIdentityCopy}>
                  <span className={styles.accountName}>{username(user)}</span>
                  <span className={styles.accountEmail}>{user.email}</span>
                </div>
                <div className={styles.accountIdentityActions}>
                  <Link
                    className={styles.attentionInfoButton}
                    to="/profile?view=attention"
                    role="menuitem"
                    aria-label="Attention state (coming soon)"
                    title="Attention state — coming soon"
                    onClick={() => setAccountOpen(false)}
                  >
                    <CircleAlert />
                    <span aria-hidden="true" className={styles.attentionInfoBadge}>Soon</span>
                  </Link>
                  <Link
                    className={styles.attentionInfoButton}
                    to="/profile?view=stats"
                    role="menuitem"
                    aria-label="User stats (coming soon)"
                    title="User stats — coming soon"
                    onClick={() => setAccountOpen(false)}
                  >
                    <BarChart3 />
                    <span aria-hidden="true" className={styles.attentionInfoBadge}>Soon</span>
                  </Link>
                  <Link
                    className={styles.attentionInfoButton}
                    to="/profile?view=costs"
                    role="menuitem"
                    aria-label="Agents cost (coming soon)"
                    title="Agents cost — coming soon"
                    onClick={() => setAccountOpen(false)}
                  >
                    <CircleDollarSign />
                    <span aria-hidden="true" className={styles.attentionInfoBadge}>Soon</span>
                  </Link>
                </div>
              </div>
              <form action="#" method="post">
                <button className={styles.signOut} type="submit" role="menuitem">
                  <LogOut />
                  Sign out
                </button>
              </form>
            </div>
          ) : null}
        </div>
      ) : null}
      <span className={styles.version}>v0.1</span>
    </nav>
  )
}

function fullscreenHintHasBeenShown() {
  try {
    return Boolean(window.localStorage.getItem(fullscreenHintStorageKey))
  } catch {
    return false
  }
}

function markFullscreenHintShown() {
  try {
    window.localStorage.setItem(fullscreenHintStorageKey, 'true')
  } catch {
    // The hint remains optional when browser storage is unavailable.
  }
}

function itemClass(active: boolean) {
  return `${styles.item}${active ? ` ${styles.active}` : ''}`
}

function userLabel(user: CurrentUser) {
  return `${username(user)} | ${user.email}`
}

function username(user: CurrentUser) {
  const displayName = user.display_name.trim()
  if (displayName) return displayName
  return user.email.split('@')[0] || user.email
}

function userInitials(user: CurrentUser) {
  const name = username(user)
  const parts = name.split(/\s+/).filter(Boolean)
  const initials = parts.length > 1 ? `${parts[0][0]}${parts[1][0]}` : name.slice(0, 2)
  return initials.toUpperCase()
}
