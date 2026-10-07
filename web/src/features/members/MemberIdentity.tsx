import type { ProjectMember } from '../../data/types'
import { useProjectMember } from './projectMemberContext'
import styles from './MemberIdentity.module.css'

export function MemberIdentity({ memberId, compact = false, className = '', linkProfile = false, showAvatar = true }: {
  memberId: string
  compact?: boolean
  className?: string
  linkProfile?: boolean
  showAvatar?: boolean
}) {
  const state = useProjectMember(memberId)
  const classes = [styles.identity, compact ? styles.compact : '', className].filter(Boolean).join(' ')

  if (state.status === 'loading') {
    return <span className={classes} aria-label={`Loading member ${memberId}`}>{showAvatar && <span className={styles.placeholderAvatar} aria-hidden="true" />}{!compact && <span>Loading member</span>}</span>
  }
  if (state.status === 'unavailable') {
    const fallback = compactMemberID(memberId)
    return <span className={classes} aria-label={`Member unavailable ${fallback}`} title={`Member unavailable: ${memberId}`}>{showAvatar && <span className={styles.placeholderAvatar} aria-hidden="true">!</span>}{!compact && <span>{fallback}</span>}</span>
  }

  if (state.status === 'missing') {
    const fallback = compactMemberID(memberId)
    return <span className={classes} aria-label={`Unknown member ${fallback}`} title={memberId}>{showAvatar && <span className={styles.placeholderAvatar} aria-hidden="true">?</span>}{!compact && <span>{fallback}</span>}</span>
  }

  return <MemberIdentityView member={state.member} compact={compact} className={className} linkProfile={linkProfile} showAvatar={showAvatar} />
}

export function MemberIdentityView({ member, compact = false, className = '', linkProfile = false, showAvatar = true }: {
  member: ProjectMember
  compact?: boolean
  className?: string
  linkProfile?: boolean
  showAvatar?: boolean
}) {
  const classes = [styles.identity, compact ? styles.compact : '', className].filter(Boolean).join(' ')
  const content = (
    <>
      {showAvatar && (member.avatar_url
        ? <img className={styles.avatar} src={member.avatar_url} alt="" />
        : <span className={styles.placeholderAvatar} aria-hidden="true">{member.login.slice(0, 1).toUpperCase()}</span>)}
      {!compact && <span>{member.login}</span>}
    </>
  )
  const label = compact ? member.login : undefined

  return linkProfile && member.profile_url
    ? <a className={classes} href={member.profile_url} rel="noreferrer" target="_blank" aria-label={label}>{content}</a>
    : <span className={classes} aria-label={label}>{content}</span>
}

function compactMemberID(memberId: string) {
  const normalized = memberId.trim()
  if (normalized.length <= 20) return normalized || 'Unknown member'
  return `${normalized.slice(0, 12)}…${normalized.slice(-5)}`
}
