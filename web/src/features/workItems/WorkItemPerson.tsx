import { useId } from 'react'
import { MemberIdentity } from '../members/MemberIdentity'
import { useProjectMember } from '../members/projectMemberContext'
import styles from './WorkItems.module.css'

export function WorkItemPerson({ memberId, role }: { memberId: string; role: string }) {
  const state = useProjectMember(memberId)
  const tooltipId = useId()
  const name = state.status === 'resolved' ? state.member.login : memberId

  return <span className={styles.person} tabIndex={0} aria-describedby={tooltipId}>
    <MemberIdentity memberId={memberId} compact />
    <span className={styles.personTooltip} id={tooltipId} role="tooltip">{name} · {role}</span>
  </span>
}
