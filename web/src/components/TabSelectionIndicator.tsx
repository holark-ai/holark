import { useLayoutEffect, useRef, type RefObject } from 'react'
import styles from './TabSelectionIndicator.module.css'

type IndicatorVariant = 'pill' | 'underline'

type TabSelectionIndicatorProps = {
  stripRef: RefObject<HTMLElement | null>
  selectedSelector?: string
  className?: string
  variant?: IndicatorVariant
}

export function TabSelectionIndicator({
  stripRef,
  selectedSelector = '[role="tab"][aria-selected="true"]',
  className,
  variant = 'pill',
}: TabSelectionIndicatorProps) {
  const indicatorRef = useRef<HTMLSpanElement>(null)

  // Measure after every render, including tab insertion, removal and renaming.
  useLayoutEffect(() => {
    const indicator = indicatorRef.current
    positionIndicator(stripRef.current ?? indicator?.parentElement ?? null, indicator, selectedSelector, variant)
  })

  useLayoutEffect(() => {
    const indicator = indicatorRef.current
    // On mount, the parent strip's ref attaches after this layout effect.
    const strip = stripRef.current ?? indicator?.parentElement
    if (!strip || !indicator) return
    const position = () => positionIndicator(strip, indicator, selectedSelector, variant, true)
    // Initial observer delivery must not interrupt a selection transition.
    let initialObservation = true
    const observer = new ResizeObserver(() => {
      if (!initialObservation) position()
      initialObservation = false
    })
    observer.observe(strip)
    strip.querySelectorAll('[role="tab"], a, button').forEach((tab) => observer.observe(tab))
    let disposed = false
    document.fonts?.ready.then(() => { if (!disposed) position() })
    return () => {
      disposed = true
      observer.disconnect()
    }
  }, [stripRef, selectedSelector, variant])

  return <span ref={indicatorRef} className={[styles.indicator, className].filter(Boolean).join(' ')} data-variant={variant} aria-hidden="true" />
}

function positionIndicator(strip: HTMLElement | null, indicator: HTMLElement | null, selectedSelector: string, variant: IndicatorVariant, immediate = false) {
  if (!strip || !indicator) return
  const selected = strip.querySelector<HTMLElement>(selectedSelector)
  indicator.hidden = !selected
  if (!selected || !strip.getBoundingClientRect().width) return
  const tabRect = selected.getBoundingClientRect()
  const stripRect = strip.getBoundingClientRect()
  const width = `${tabRect.width}px`
  const height = variant === 'underline' ? '2px' : `${tabRect.height}px`
  const top = variant === 'underline' ? tabRect.bottom - 1 : tabRect.top
  const transform = `translate(${tabRect.left - stripRect.left + strip.scrollLeft}px, ${top - stripRect.top + strip.scrollTop}px)`
  const previousWidth = indicator.style.width
  const previousHeight = indicator.style.height
  const previousTransform = indicator.style.transform
  immediate ||= !indicator.style.width
  indicator.style.width = width
  indicator.style.height = height
  indicator.style.transform = transform
  // Compare browser-serialized values: CSS rounds fractional pixel values.
  // A resize notification for unchanged geometry must not cancel the slide.
  if (indicator.style.width === previousWidth && indicator.style.height === previousHeight && indicator.style.transform === previousTransform) return
  if (immediate) {
    indicator.style.transition = 'none'
    void indicator.offsetWidth
    indicator.style.transition = ''
  }
}
