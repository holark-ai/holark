import { ChevronRight } from 'lucide-react'
import { useContext, useEffect, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { isSessionResumable } from '../../data/harness'
import { api } from '../../data/api'
import { OperationsContext } from '../operations/operationsContext'
import { useOptionalHolonStore } from '../project/holonStoreContext'
import { holonDisplayName, holonNeedsInput, holonDisplayStatus, holonDisplayStatusLabel } from './holonDisplay'
import { formatRelativeTime, formatTime, inputStateLabel, sessionStatusClassName } from './holonRowDisplay'
import { HolonStatusIcon } from './HolonStatusIcon'
import styles from './HolonTile.module.css'

export function HolonTile({ holonId, label, compact = false }: {
  holonId: string
  label: string
  compact?: boolean
}) {
  const store = useOptionalHolonStore()
  const location = useLocation()
  const operations = useContext(OperationsContext)
  const holon = store?.getHolon(holonId)
  const name = holon ? holonDisplayName(holon) : undefined
  const status = holon ? holonDisplayStatus(holon) : undefined
  const needsInput = Boolean(holon && holonNeedsInput(holon))
  const stateLabel = holon
    ? inputStateLabel(holon) || (holon.archived_at && isSessionResumable(holon) && status !== 'finalizing' ? 'Workspace archived.' : holonDisplayStatusLabel(holon))
    : 'Loading holon'
  const [hovered, setHovered] = useState(false)
  const [focused, setFocused] = useState(false)
  const showPreview = operations?.showPreview
  const clearPreview = operations?.clearPreview
  const updateHolon = store?.updateHolon

  useEffect(() => {
    if (holon || !updateHolon) return
    let cancelled = false
    void api.holon(holonId).then((loaded) => {
      if (!cancelled) updateHolon(holonId, (current) => current ?? loaded)
    }).catch(() => {
      // The shared store keeps polling if this initial request fails.
    })
    return () => { cancelled = true }
  }, [holon, holonId, updateHolon])

  useEffect(() => {
    if ((!hovered && !focused) || !showPreview || !clearPreview) return
    const preview = { holonId }
    const timer = window.setTimeout(() => showPreview(preview), 200)
    return () => {
      window.clearTimeout(timer)
      clearPreview(preview)
    }
  }, [holonId, hovered, focused, showPreview, clearPreview])

  return (
    <Link
      to={`/holons/${holonId}`}
      state={{ focusHolonTerminal: true }}
      onClick={(event) => {
        if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
        store?.activateHolon(holonId, location.pathname === `/holons/${holonId}`)
      }}
      className={`${styles.tile} ${compact ? styles.compact : ''}`}
      data-holon-tile={holonId}
      aria-label={holon ? `${label}: ${name}, ${stateLabel}` : `${label}: Loading holon`}
      aria-busy={!holon}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      onFocus={() => setFocused(true)}
      onBlur={() => setFocused(false)}
    >
      {status
        ? <HolonStatusIcon address={operations?.addresses.get(holonId)} status={status} needsInput={needsInput} />
        : <span className={styles.skeletonBadge} aria-hidden="true" />}
      {holon ? (
        <span className={styles.copy}>
          <strong className={styles.name} title={name}>{name}</strong>
          <span className={styles.meta}>
            <span className={status ? sessionStatusClassName(status, needsInput) : undefined}>{stateLabel}</span>
            {holon.created_at && <><span aria-hidden="true">·</span><time dateTime={holon.created_at} title={formatTime(holon.created_at)}>{formatRelativeTime(holon.created_at)}</time></>}
          </span>
          {status === 'finalizing' && holon.reason && <span className={styles.meta}>{holon.reason}</span>}
        </span>
      ) : (
        <span className={styles.copy} aria-hidden="true">
          <span className={styles.skeletonName} />
          <span className={styles.skeletonStatus} />
        </span>
      )}
      <ChevronRight className={styles.chevron} aria-hidden="true" />
    </Link>
  )
}
