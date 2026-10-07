import { UserRound } from 'lucide-react'
import { Link, useLocation } from 'react-router-dom'
import { useGitHubProfile } from './githubProfileContext'
import styles from './Profile.module.css'

export function ProfileLink() {
  const { data: profile } = useGitHubProfile()
  const { pathname } = useLocation()

  return (
    <Link
      className={styles.profileLink}
      to="/profile"
      aria-label="Open profile"
      aria-current={pathname === '/profile' ? 'page' : undefined}
      title={profile ? profile.name || `@${profile.login}` : 'Your account'}
    >
      {profile?.avatar_url
        ? <img className={styles.linkAvatar} src={profile.avatar_url} width={34} height={34} alt="" />
        : <UserRound aria-hidden="true" />}
    </Link>
  )
}
