import * as Select from '@radix-ui/react-select'
import { Check, ChevronDown } from 'lucide-react'
import { useId } from 'react'
import type { HarnessType } from '../../data/types'
import styles from './SettingsView.module.css'

type PermissionChoice = { value: string; label: string; description: string }

const native: PermissionChoice = { value: 'inherit', label: 'Inherit native settings', description: 'Uses the harness’s user, project, and managed permission configuration.' }
const choices: Record<HarnessType, PermissionChoice[]> = {
  codex: [
    native,
    { value: 'workspace-write', label: 'Workspace writes', description: 'Workspace-write sandbox with approval on request. Native network rules and approval reviewer still apply.' },
    { value: 'read-only', label: 'Read-only sandbox', description: 'Read-only sandbox; Codex may request approval to write or run outside it.' },
    { value: 'full-access', label: 'Full access', description: 'Disables the sandbox and approval prompts. Commands can access files and the network as your user.' },
  ],
  'claude-code': [
    native,
    { value: 'default', label: 'Ask for permission', description: 'Uses Claude’s default permission mode. Native allow and deny rules still apply.' },
    { value: 'acceptEdits', label: 'Accept edits', description: 'Automatically accepts file edits; other actions follow Claude’s permission rules.' },
    { value: 'plan', label: 'Plan', description: 'Explore and plan without editing source files.' },
    { value: 'dontAsk', label: 'Deny unapproved actions', description: 'Denies actions that would require a permission prompt. Pre-approved actions can still run.' },
    { value: 'bypassPermissions', label: 'Bypass permissions', description: 'Skips permission checks, subject to managed restrictions. Use only in an isolated environment.' },
  ],
  opencode: [
    native,
    { value: 'ask', label: 'Ask by default', description: 'Sets OpenCode’s default tool permission to ask. More specific native and agent rules may take precedence.' },
    { value: 'allow', label: 'Allow by default', description: 'Sets OpenCode’s default tool permission to allow. This does not add a sandbox; more specific rules may take precedence.' },
    { value: 'deny', label: 'Deny by default', description: 'Sets OpenCode’s default tool permission to deny. More specific native and agent rules may take precedence.' },
  ],
}

export function PermissionPreferenceSelect({ label, harness, value, inherited, disabled, onSave }: {
  label: string
  harness: HarnessType
  value: string
  inherited?: boolean
  disabled: boolean
  onSave: (permissions: string) => Promise<boolean>
}) {
  const descriptionID = useId()
  const options = choices[harness]
  const selectedValue = value || (harness === 'codex' ? 'workspace-write' : native.value)
  const selected = options.find((option) => option.value === selectedValue)
  const selectedLabel = selected?.label ?? selectedValue
  if (inherited) return <div className={styles.inheritedModel} aria-label={`${label} permissions`} title={selected?.description}><small>Inherited permissions</small><span>{selectedLabel}</span></div>
  return <div className={styles.modelPreference}>
    <Select.Root value={selectedValue} disabled={disabled} onValueChange={(selection) => void onSave(selection)}>
      <Select.Trigger className={styles.harnessTrigger} aria-label={`${label} permissions`} aria-describedby={descriptionID} title={selectedLabel}>
        <span className={styles.triggerCopy}><small>Permissions</small><strong>{selectedLabel}</strong></span>
        <Select.Icon asChild><ChevronDown aria-hidden="true" /></Select.Icon>
      </Select.Trigger>
      <Select.Portal>
        <Select.Content className={styles.harnessContent} position="popper" sideOffset={6} align="end" collisionPadding={10}>
          <Select.Viewport className={styles.harnessViewport}>
            {options.map((option) => <Select.Item className={styles.harnessOption} key={option.value} value={option.value} title={option.description}>
              <Select.ItemText>{option.label}</Select.ItemText>
              <Select.ItemIndicator className={styles.harnessIndicator}><Check aria-hidden="true" /></Select.ItemIndicator>
            </Select.Item>)}
          </Select.Viewport>
        </Select.Content>
      </Select.Portal>
    </Select.Root>
    <p id={descriptionID} className={styles.permissionDescription}>{selected?.description}</p>
  </div>
}
