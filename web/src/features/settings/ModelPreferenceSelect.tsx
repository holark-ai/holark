import * as Select from '@radix-ui/react-select'
import { Check, ChevronDown } from 'lucide-react'
import { useRef, useState } from 'react'
import { Button } from '../../components/Button'
import type { ModelDiscovery } from './useModelChoices'
import styles from './SettingsView.module.css'

export function ModelPreferenceSelect({ label, value, inherited, disabled, discovery, load, onSave }: {
  label: string
  value: string
  inherited?: boolean
  disabled: boolean
  discovery?: ModelDiscovery
  load: (retry?: boolean) => void
  onSave: (model: string) => Promise<boolean>
}) {
  const [custom, setCustom] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const focusCustom = useRef(false)
  const [draft, setDraft] = useState(value)
  const models = discovery?.models ?? []
  const selectedLabel = models.find((model) => model.id === value)?.label ?? (value || 'Agent default')
  if (inherited) return <div className={styles.inheritedModel} aria-label={`${label} model`} title={value || 'Agent default'}><small>Inherited model</small><span>{selectedLabel}</span></div>
  const save = async (model: string) => { if (await onSave(model)) setCustom(false) }
  return <div className={styles.modelPreference}>
    <Select.Root value={custom ? 'custom' : value ? `model:${value}` : 'default'} disabled={disabled} onOpenChange={(open) => { if (open) load() }} onValueChange={(selection) => {
      if (selection === 'custom') { focusCustom.current = true; setDraft(value); setCustom(true) }
      else void save(selection === 'default' ? '' : selection.slice(6))
    }}>
      <Select.Trigger className={styles.harnessTrigger} aria-label={`${label} model`} title={selectedLabel}>
        <span className={styles.triggerCopy}><small>Model</small><strong>{custom ? 'Custom model…' : selectedLabel}</strong></span>
        <Select.Icon asChild><ChevronDown aria-hidden="true" /></Select.Icon>
      </Select.Trigger>
      <Select.Portal>
        <Select.Content className={styles.harnessContent} position="popper" sideOffset={6} align="end" collisionPadding={10} onCloseAutoFocus={(event) => {
          if (focusCustom.current) {
            event.preventDefault()
            focusCustom.current = false
            window.requestAnimationFrame(() => input.current?.focus())
          }
        }}>
          <Select.Viewport className={styles.harnessViewport}>
            {[{ id: 'default', label: 'Agent default' },
              ...(value && !models.some((model) => model.id === value) ? [{ id: `model:${value}`, label: value }] : []),
              ...models.map((model) => ({ id: `model:${model.id}`, label: model.label })),
              { id: 'custom', label: 'Custom model…' }].map((option) => <Select.Item className={styles.harnessOption} key={option.id} value={option.id}>
                <Select.ItemText>{option.label}</Select.ItemText>
                <Select.ItemIndicator className={styles.harnessIndicator}><Check aria-hidden="true" /></Select.ItemIndicator>
              </Select.Item>)}
          </Select.Viewport>
        </Select.Content>
      </Select.Portal>
    </Select.Root>
    {discovery?.loading && <small role="status">Loading models…</small>}
    {discovery?.error && <div className={styles.modelRetry}><small role="status">Could not load models.</small><Button size="small" variant="ghost" onClick={() => load(true)}>Retry</Button></div>}
    {custom && <form className={styles.customModel} onSubmit={(event) => { event.preventDefault(); void save(draft.trim()) }}>
      <input ref={input} aria-label={`${label} custom model ID`} placeholder="Model ID" value={draft} maxLength={256} onChange={(event) => setDraft(event.target.value)} autoFocus />
      <Button type="submit" size="small" disabled={disabled || !draft.trim() || /\s|\p{Cc}/u.test(draft.trim())}>Save</Button>
    </form>}
  </div>
}
