import { BarChart3, CircleDollarSign, Focus, RefreshCw, UserRound } from 'lucide-react'
import { useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { ProjectHeader } from '../project/ProjectHeader'
import { useGitHubProfile } from './githubProfileContext'
import styles from './Profile.module.css'

const sections = {
  attention: {
    label: 'Attention state',
    icon: Focus,
  },
  stats: {
    label: 'User stats',
    icon: BarChart3,
  },
  costs: {
    label: 'Agents cost',
    icon: CircleDollarSign,
  },
}

export function ProfilePage() {
  const { data: profile, loading, error, refresh } = useGitHubProfile()
  const [syncing, setSyncing] = useState(false)
  const name = profile ? profile.name || `@${profile.login}` : undefined
  const [searchParams] = useSearchParams()
  const requestedSection = searchParams.get('view')
  const activeSection = requestedSection === 'stats' || requestedSection === 'costs' ? requestedSection : 'attention'
  const section = sections[activeSection]
  const Icon = section.icon
  const sync = async () => {
    if (loading || syncing) return
    setSyncing(true)
    try {
      await refresh()
    } finally {
      setSyncing(false)
    }
  }

  return (
    <div>
      <ProjectHeader />
      <div className={styles.page}>
        <header className={styles.pageHeader}>
          <div className={styles.avatar}>
            {profile?.avatar_url
              ? <img src={profile.avatar_url} width={64} height={64} alt={`GitHub avatar for ${name}`} />
              : <UserRound aria-hidden="true" />}
          </div>
          <div className={styles.identity}>
            <h1>{name || 'Profile'}</h1>
            {profile ? (
              <p className={styles.githubIdentity}>
                {profile.name && <><span>GitHub</span><span aria-hidden="true">·</span></>}
                <a href={profile.profile_url || `https://github.com/${encodeURIComponent(profile.login)}`} target="_blank" rel="noreferrer">
                  {profile.name ? `@${profile.login}` : 'GitHub'}
                </a>
              </p>
            ) : (
              <p className={styles.profileStatus} title={error?.message}>{loading ? 'Syncing GitHub…' : 'GitHub unavailable'}</p>
            )}
          </div>
          <button
            className={styles.syncProfile}
            type="button"
            aria-label="Sync GitHub profile"
            aria-busy={loading || syncing}
            title="Sync GitHub profile"
            disabled={loading || syncing}
            onClick={() => void sync()}
          >
            <RefreshCw aria-hidden="true" />
          </button>
        </header>
        <nav className={styles.sections} aria-label="Profile sections">
          {Object.entries(sections).map(([key, item]) => (
            <Link key={key} to={`/profile?view=${key}`} aria-current={activeSection === key ? 'page' : undefined}>
              <item.icon aria-hidden="true" />
              {item.label}
            </Link>
          ))}
        </nav>
        <section className={styles.emptyState} aria-labelledby="profile-section-title">
          <Icon className={styles.emptyIcon} aria-hidden="true" />
          <span className={styles.comingSoon}>Coming soon</span>
          <h2 id="profile-section-title">{section.label}</h2>
        </section>
      </div>
    </div>
  )
}
