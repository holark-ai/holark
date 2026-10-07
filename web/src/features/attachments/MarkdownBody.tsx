import { memo, useEffect, useMemo, useState, type ComponentProps } from 'react'
import ReactMarkdown from 'react-markdown'
import rehypeRaw from 'rehype-raw'
import rehypeSanitize from 'rehype-sanitize'
import remarkGfm from 'remark-gfm'
import { api } from '../../data/api'
import { Button } from '../../components/Button'
import styles from './Markdown.module.css'

export const MarkdownBody = memo(function MarkdownBody({ value, className }: { value: string, className?: string }) {
  return <div className={`${styles.body} ${className ?? ''}`}>
    <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeRaw, rehypeSanitize]} components={{ img: MarkdownImage, source: MarkdownSource }}>{value}</ReactMarkdown>
  </div>
})

function isGitHubAssetURL(value: string) {
  return /^https:\/\/github\.com\/(?:user-attachments\/assets|[a-z0-9-]+\/[a-z0-9_.-]+\/assets\/[0-9]+)\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value)
}

function gitHubSrcSetCandidates(value: string) {
  const candidates: { url: string, start: number, end: number }[] = []
  let position = 0
  while (position < value.length) {
    while (position < value.length && /[\t\n\f\r ,]/.test(value[position])) position++
    const start = position
    while (position < value.length && !/[\t\n\f\r ]/.test(value[position])) position++
    const token = value.slice(start, position)
    const url = token.replace(/,+$/, '')
    if (isGitHubAssetURL(url)) candidates.push({ url, start, end: start + url.length })
    if (token.endsWith(',')) continue
    // Commas within URLs are not candidate separators. Skip descriptors up
    // to the next separator, including any future descriptors in parentheses.
    let inParens = false
    while (position < value.length) {
      const character = value[position++]
      if (character === '(') inParens = true
      else if (character === ')') inParens = false
      else if (character === ',' && !inParens) break
    }
  }
  return candidates
}

function MarkdownSource({ srcSet, media, type, sizes, width, height, title }: ComponentProps<'source'>) {
  const source = typeof srcSet === 'string' ? srcSet : ''
  const candidates = useMemo(() => gitHubSrcSetCandidates(source), [source])
  const [resolution, setResolution] = useState<{ source: string, srcSet: string }>()
  useEffect(() => {
    if (!candidates.length) return
    const controller = new AbortController()
    void Promise.all(candidates.map(async ({ url }) => {
      try { return (await api.resolveGitHubImage(url, controller.signal)).url }
      catch { return url } // Keep public attachments usable without a GitHub login.
    })).then((urls) => {
      if (controller.signal.aborted) return
      let displaySrcSet = source
      for (let index = candidates.length - 1; index >= 0; index--) {
        const { start, end } = candidates[index]
        displaySrcSet = displaySrcSet.slice(0, start) + urls[index] + displaySrcSet.slice(end)
      }
      setResolution({ source, srcSet: displaySrcSet })
    })
    return () => controller.abort()
  }, [source, candidates])

  // Let picture use its fallback while private sources are being resolved.
  const displaySrcSet = candidates.length ? (resolution?.source === source ? resolution.srcSet : undefined) : srcSet
  return <source srcSet={displaySrcSet} media={media} type={type} sizes={sizes} width={width} height={height} title={title} />
}

function MarkdownImage({ src, alt, title, width, height }: ComponentProps<'img'>) {
  const source = typeof src === 'string' ? src : ''
  const needsResolution = isGitHubAssetURL(source)
  const [resolution, setResolution] = useState<{ source: string, url?: string, error?: string }>()
  const [retry, setRetry] = useState({ source, count: 0 })
  const attempt = retry.source === source ? retry.count : 0
  useEffect(() => {
    if (!needsResolution) return
    const controller = new AbortController()
    void api.resolveGitHubImage(source, controller.signal).then(({ url }) => {
      if (!controller.signal.aborted) setResolution({ source, url })
    }).catch((error: unknown) => {
      // Public attachments remain viewable even if the local GitHub login is
      // unavailable. Private attachments still require a resolved signed URL.
      if (!controller.signal.aborted) setResolution({ source, url: source, error: error instanceof Error ? error.message : 'Image unavailable.' })
    })
    return () => controller.abort()
  }, [source, needsResolution, attempt])

  const current = resolution?.source === source ? resolution : undefined
  const error = needsResolution && !current?.url ? current?.error : undefined
  if (error) return <span className={styles.imageNotice}>
    <span>{alt || 'Image'}: {error}</span>
    <a href={source} target="_blank" rel="noreferrer">Open on GitHub</a>
    <Button size="small" variant="ghost" onClick={() => { setResolution(undefined); setRetry({ source, count: attempt + 1 }) }}>Retry image</Button>
  </span>

  const displayURL = needsResolution ? current?.url : source
  return <>
    {needsResolution && !displayURL && <span role="status" className={styles.imageNotice}>Loading image…</span>}
    <img className={styles.image} src={displayURL || undefined} alt={alt ?? ''} title={title} width={width} height={height} loading="lazy" referrerPolicy="no-referrer" onError={() => {
      if (!needsResolution) return
      // A lazy-loaded private image may outlive its signed URL. Resolve it once
      // more when it enters the viewport; subsequent failures offer a retry.
      if (attempt === 0) { setResolution(undefined); setRetry({ source, count: 1 }) }
      else setResolution({ source, error: current?.error || 'The image could not be loaded.' })
    }} />
  </>
}
