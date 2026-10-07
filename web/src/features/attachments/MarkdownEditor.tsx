import { Eye, Paperclip, Pencil } from 'lucide-react'
import { forwardRef, useEffect, useLayoutEffect, useRef, useState, type ReactNode, type TextareaHTMLAttributes } from 'react'
import { Button } from '../../components/Button'
import { api } from '../../data/api'
import { MarkdownBody } from './MarkdownBody'
import styles from './Markdown.module.css'

type Props = Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, 'value' | 'onChange' | 'onPaste' | 'onDrop'> & {
  value: string
  onChange: (value: string) => void
  onUploadingChange?: (uploading: boolean) => void
  previewLabel?: string
  editLabel?: string
  previewRegionLabel?: string
  previewResetKey?: string
  autoResize?: boolean
  actions?: ReactNode
  // Show the toolbar only once the field is focused or has content.
  revealToolbar?: boolean
  // Shown above the field while the toolbar is revealed.
  header?: ReactNode
}

const maximumImageBytes = 10 * 1024 * 1024
const acceptImages = '.png,.jpg,.jpeg,.gif,.webp,.svg'
const exampleAssetURL = 'https://github.com/user-attachments/assets/00000000-0000-0000-0000-000000000000'

export const MarkdownEditor = forwardRef<HTMLTextAreaElement, Props>(function MarkdownEditor({
  value, onChange, onUploadingChange, disabled, maxLength, previewLabel = 'Preview Markdown', editLabel = 'Edit Markdown',
  previewRegionLabel = 'Markdown preview', previewResetKey, autoResize = false, actions, revealToolbar = false, header, className, ...textareaProps
}, forwardedRef) {
  const textarea = useRef<HTMLTextAreaElement>(null)
  const picker = useRef<HTMLInputElement>(null)
  const activeUpload = useRef<AbortController | null>(null)
  const latest = useRef({ value, onChange, onUploadingChange })
  useLayoutEffect(() => { latest.current = { value, onChange, onUploadingChange } })
  const [previewState, setPreviewState] = useState<{ key?: string, visible: boolean }>({ key: previewResetKey, visible: false })
  if (previewState.key !== previewResetKey) setPreviewState({ key: previewResetKey, visible: false })
  const preview = previewState.key === previewResetKey && previewState.visible
  const setPreview = (visible: boolean) => setPreviewState({ key: previewResetKey, visible })
  const [uploading, setUploading] = useState(false)
  const [progress, setProgress] = useState('')
  const [error, setError] = useState('')
  const [dragging, setDragging] = useState(false)
  const [focused, setFocused] = useState(false)
  const showToolbar = !revealToolbar || focused || value !== '' || preview || uploading

  useEffect(() => () => {
    activeUpload.current?.abort()
    if (activeUpload.current) latest.current.onUploadingChange?.(false)
  }, [])

  useLayoutEffect(() => {
    const field = textarea.current
    if (!autoResize || !field || preview) return
    const resize = () => { field.style.height = 'auto'; field.style.height = `${field.scrollHeight}px` }
    resize()
    let width = field.getBoundingClientRect().width
    const observer = new ResizeObserver(() => {
      const nextWidth = field.getBoundingClientRect().width
      if (width === nextWidth) return
      width = nextWidth
      resize()
    })
    observer.observe(field)
    return () => observer.disconnect()
  }, [autoResize, value, preview])

  const upload = async (chosenFiles: File[]) => {
    if (disabled || activeUpload.current || !chosenFiles.length) return
    const files = chosenFiles.map(normalizeClipboardFile)
    setDragging(false)
    setError('')
    if (files.length > 50) { setError('Choose at most 50 images at a time.'); return }
    if (files.some((file) => file.size > maximumImageBytes)) { setError('Images must be no larger than 10 MB.'); return }
    if (files.some((file) => !/\.(png|jpe?g|gif|webp|svg)$/i.test(file.name))) { setError('Choose a PNG, JPEG, GIF, WebP, or SVG image.'); return }
    const start = textarea.current?.selectionStart ?? value.length
    const end = textarea.current?.selectionEnd ?? start
    const expected = insertImages(value, start, end, files.map((file) => imageMarkdown(file.name, exampleAssetURL)))
    if (maxLength !== undefined && expected.body.length > maxLength) { setError('There is not enough room in this draft for these image references. Shorten the text first.'); return }

    const controller = new AbortController()
    activeUpload.current = controller
    setUploading(true)
    latest.current.onUploadingChange?.(true)
    let cursor: number | undefined
    const failures: string[] = []
    try {
      for (const [index, file] of files.entries()) {
        if (controller.signal.aborted) return
        setProgress(`Uploading image ${index + 1} of ${files.length}…`)
        try {
          const asset = await api.uploadGitHubImage(file, controller.signal)
          if (controller.signal.aborted) return
          const inserted = insertImages(latest.current.value, cursor ?? start, cursor ?? end, [imageMarkdown(file.name, asset.url)])
          cursor = inserted.cursor
          // Save each result to the draft, even before React renders the update.
          latest.current.value = inserted.body
          latest.current.onChange(inserted.body)
        } catch (error) {
          if (controller.signal.aborted) return
          failures.push(`${file.name}: ${error instanceof Error ? error.message : 'Upload failed.'}`)
        }
      }
      if (controller.signal.aborted) return
      if (cursor !== undefined) {
        const insertedCursor = cursor
        setPreview(false)
        requestAnimationFrame(() => {
          textarea.current?.focus()
          textarea.current?.setSelectionRange(insertedCursor, insertedCursor)
        })
      }
      setError(failures.join(' '))
    } finally {
      if (!controller.signal.aborted) {
        activeUpload.current = null
        setUploading(false)
        setProgress('')
        latest.current.onUploadingChange?.(false)
      }
    }
  }

  const toolbar = showToolbar && <div className={styles.toolbar}>
    <Button size="small" variant="ghost" icon={preview ? <Pencil /> : <Eye />} aria-label={preview ? editLabel : previewLabel} title={preview ? editLabel : previewLabel} aria-pressed={preview} disabled={uploading} onClick={() => setPreview(!preview)}>{preview ? 'Write' : 'Preview'}</Button>
    <Button size="small" variant="ghost" icon={<Paperclip />} disabled={disabled || uploading} onClick={() => picker.current?.click()}>Attach image</Button>
    {actions && <span className={styles.actions}>{actions}</span>}
  </div>

  return <div data-markdown-editor className={styles.editor} data-dragging={dragging || undefined} data-active={(revealToolbar && showToolbar) || undefined}
    onFocus={() => setFocused(true)}
    onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setFocused(false) }}
    onDragOver={(event) => { if (!disabled && event.dataTransfer.types.includes('Files')) { event.preventDefault(); event.dataTransfer.dropEffect = 'copy' } }}
    onDragEnter={(event) => { if (!disabled && event.dataTransfer.types.includes('Files')) setDragging(true) }}
    onDragLeave={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDragging(false) }}
    onDrop={(event) => {
      setDragging(false)
      if (!event.dataTransfer.files.length) return
      event.preventDefault()
      void upload(Array.from(event.dataTransfer.files))
    }}>
    {revealToolbar && showToolbar && header}
    {preview ? <section data-markdown-preview className={styles.preview} aria-label={previewRegionLabel}>
      {value.trim() ? <MarkdownBody value={value} /> : <p>Nothing to preview yet.</p>}
    </section> : <textarea {...textareaProps} className={className} ref={(field) => {
      textarea.current = field
      if (typeof forwardedRef === 'function') forwardedRef(field)
      else if (forwardedRef) forwardedRef.current = field
    }} value={value} disabled={disabled || uploading} maxLength={maxLength} onChange={(event) => onChange(event.target.value)} onPaste={(event) => {
      const images = Array.from(event.clipboardData.items).filter((item) => item.kind === 'file' && item.type.startsWith('image/')).map((item) => item.getAsFile()).filter((file): file is File => Boolean(file))
      if (!images.length) return
      event.preventDefault()
      void upload(images)
    }} />}
    <input ref={picker} type="file" aria-label="Choose images" accept={acceptImages} multiple hidden onChange={(event) => { const files = Array.from(event.target.files ?? []); event.target.value = ''; void upload(files) }} />
    {toolbar}
    {progress && <p className={styles.notice} role="status">{progress}</p>}
    {error && <p className={styles.notice} role="alert">{error}</p>}
  </div>
})

function imageMarkdown(name: string, url: string) {
  const alt = name.replace(/[[\]\\]/g, '\\$&').replace(/[\r\n]/g, ' ')
  return `![${alt}](${url})`
}

function normalizeClipboardFile(file: File) {
  // Some browsers omit the filename extension on pasted screenshots.
  if (/\.[^./\\]+$/.test(file.name)) return file
  const extensions: Record<string, string> = { 'image/png': 'png', 'image/jpeg': 'jpg', 'image/gif': 'gif', 'image/webp': 'webp', 'image/svg+xml': 'svg' }
  const extension = extensions[file.type]
  return extension ? new File([file], `${file.name || 'image'}.${extension}`, { type: file.type, lastModified: file.lastModified }) : file
}

function insertImages(value: string, start: number, end: number, images: string[]) {
  const before = value.slice(0, start)
  const after = value.slice(end)
  const inserted = `${before && !before.endsWith('\n') ? '\n\n' : ''}${images.join('\n\n')}${after && !after.startsWith('\n') ? '\n\n' : ''}`
  return { body: before + inserted + after, cursor: before.length + inserted.length }
}
