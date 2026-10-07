import * as Select from '@radix-ui/react-select'
import { Bot, Check, ChevronDown, FileText, Gauge, GitBranch, GitPullRequest, MessageSquareReply, Moon, RotateCcw, Save, Sun, WandSparkles } from 'lucide-react'
import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useColorTheme, type ColorTheme } from '../../app/colorTheme'
import { defaultContextTokenThresholds, useContextTokenThresholds, type ContextTokenThresholds } from '../../app/contextTokenThresholds'
import { Button } from '../../components/Button'
import { api } from '../../data/api'
import { harnessDisplayLabel, harnessVersionDetail } from '../../data/harness'
import { PermissionPreferenceSelect } from './PermissionPreferenceSelect'
import { ModelPreferenceSelect } from './ModelPreferenceSelect'
import { useModelChoices } from './useModelChoices'
import { HarnessWarning } from '../agents/HarnessWarning'
import type { HarnessCapabilities, HarnessCapability, HarnessType, HarnessWorkflow, PromptTemplate } from '../../data/types'
import { useAgentCapabilities } from '../../data/useAgentCapabilities'
import { ProjectHeader } from '../project/ProjectHeader'
import styles from './SettingsView.module.css'

type PreferenceValue = HarnessType | 'inherit'

const taskWorkflows: Array<{ workflow: Exclude<HarnessWorkflow, 'default'>; label: string; icon: ReactNode }> = [
  { workflow: 'issue', label: 'Work on an issue', icon: <Bot /> },
  { workflow: 'pull_request_review', label: 'Review pull requests', icon: <GitPullRequest /> },
  { workflow: 'pull_request_feedback', label: 'Address PR feedback', icon: <MessageSquareReply /> },
  { workflow: 'pull_request_metadata', label: 'Write PR title and description', icon: <FileText /> },
  { workflow: 'pull_request_rebase', label: 'Rebase pull requests', icon: <GitBranch /> },
]

const contextScaleSteps = [250, 500, 1_000]
const contextScaleExpansionPoint = 0.9
const contextSliderStep = 1

function thresholdInThousands(value: number) {
  return value / 1_000
}

function contextScaleFor(value: number) {
  return contextScaleSteps.find((maximum) => value < maximum * contextScaleExpansionPoint) ?? contextScaleSteps.at(-1)!
}

function nextContextScale(maximum: number) {
  return contextScaleSteps.find((candidate) => candidate > maximum) ?? maximum
}

function previousContextScale(maximum: number) {
  const currentIndex = contextScaleSteps.indexOf(maximum)
  return contextScaleSteps[Math.max(0, currentIndex - 1)] ?? maximum
}

function contextScaleLabel(value: number) {
  return value === 1_000 ? '1m' : `${value}k`
}

export function SettingsView() {
  const { theme, setTheme } = useColorTheme()
  const { thresholds: contextTokenThresholds, setThresholds: setContextTokenThresholds } = useContextTokenThresholds()
  const [templates, setTemplates] = useState<PromptTemplate[]>([])
  const { data, load: loadCapabilities, setHarnessDefault } = useAgentCapabilities()
  const capabilities: HarnessCapabilities = data ?? []
  const defaults = capabilities.harness_defaults ?? {}
  const generalHarness = defaults.default?.harness_type ?? capabilities.default_harness ?? 'codex'
  const models = useModelChoices()
  const [savingWorkflow, setSavingWorkflow] = useState<HarnessWorkflow>()
  const [resettingHarnesses, setResettingHarnesses] = useState(false)
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [savingKey, setSavingKey] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [loaded] = await Promise.all([api.promptTemplates(), loadCapabilities()])
      setTemplates(loaded)
      setDrafts(Object.fromEntries(loaded.map((template) => [template.key, template.value])))
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setLoading(false)
    }
  }, [loadCapabilities])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load() })
    return () => window.clearTimeout(timer)
  }, [load])

  const updateTemplate = async (template: PromptTemplate, value: string) => {
    setSavingKey(template.key)
    setError('')
    setMessage('')
    try {
      const updated = await api.updatePromptTemplate(template.key, value)
      setTemplates((current) => current.map((item) => item.key === updated.key ? updated : item))
      setDrafts((current) => ({ ...current, [updated.key]: updated.value }))
      setMessage(`${updated.name} saved.`)
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setSavingKey('')
    }
  }

  const updateHarness = async (workflow: HarnessWorkflow, value: PreferenceValue, model = '', permissions = ''): Promise<boolean> => {
    const current = defaults[workflow]
    if (resettingHarnesses || savingWorkflow || (value === 'inherit' ? !current?.explicit : current?.explicit && current.harness_type === value && (current.model ?? '') === model && (current.permissions ?? '') === permissions)) return false
    setSavingWorkflow(workflow)
    setError('')
    try {
      if (value === 'inherit') {
        await api.resetAgentHarnessDefault(workflow)
        setHarnessDefault(workflow)
      } else {
        await api.updateAgentHarnessDefault(workflow, value, model, permissions)
        setHarnessDefault(workflow, value, model, permissions)
      }
      return true
    } catch (reason) {
      setError(errorMessage(reason))
      return false
    } finally {
      setSavingWorkflow(undefined)
    }
  }

  const resetAllHarnesses = async () => {
    const customizedWorkflows = taskWorkflows
      .map(({ workflow }) => workflow)
      .filter((workflow) => defaults[workflow]?.explicit)
    if (resettingHarnesses || savingWorkflow || customizedWorkflows.length === 0) return
    setResettingHarnesses(true)
    setError('')
    try {
      await Promise.all(customizedWorkflows.map((workflow) => api.resetAgentHarnessDefault(workflow)))
      customizedWorkflows.forEach((workflow) => setHarnessDefault(workflow))
    } catch (reason) {
      setError(errorMessage(reason))
    } finally {
      setResettingHarnesses(false)
    }
  }

  const customizedHarnessCount = taskWorkflows.filter(({ workflow }) => defaults[workflow]?.explicit).length

  return (
    <div>
      <ProjectHeader />
      <main className={styles.page}>
        <header className={styles.pageHeader}>
          <span className={styles.eyebrow}>Global</span>
          <h1>Settings</h1>
          <p>Choose how Holark looks and which coding agents it uses across your work.</p>
        </header>

        {loading && <p className={styles.message}>Loading settings…</p>}
        {error && <p className={styles.error} role="alert">{error}</p>}
        {message && <p className={styles.message} role="status">{message}</p>}

        <section className={styles.section} aria-labelledby="agent-harness-title">
          <header className={styles.sectionHeader}>
            <div className={styles.sectionIcon}><WandSparkles aria-hidden="true" /></div>
            <div>
              <h2 id="agent-harness-title">Agent harnesses</h2>
              <p>Set a harness, model, and permissions for new work, then override them for individual tasks. Existing sessions keep their saved selections. Inherited permissions follow the native configuration at launch.</p>
            </div>
          </header>

          <div className={styles.installations} aria-label="Installed harnesses">
            {capabilities.map((capability) => <div className={styles.installation} key={capability.type}>
              <strong>{harnessDisplayLabel(capability.type)}</strong>
              <span>{capability.available ? harnessVersionDetail(capability) : capability.unavailable_reason || 'Unavailable'}</span>
              <HarnessWarning capability={capability} />
              {!capability.warning && capability.latest_supported_version && <small>Holark supports {harnessDisplayLabel(capability.type)} &lt;= {capability.latest_supported_version}.</small>}
            </div>)}
          </div>

          <LaunchCommandSettings onSaved={() => { models.reset(); return loadCapabilities(true) }} />

          <div className={styles.defaultPreference}>
            <div className={styles.preferenceCopy}>
              <strong>Default harness</strong>
              <span>Used for new agents and anywhere without an override.</span>
            </div>
            <div className={styles.preferenceControls}>
            <HarnessPreferenceSelect
              workflow="default"
              value={generalHarness}
              effectiveHarness={generalHarness}
              capabilities={capabilities}
              saving={resettingHarnesses || Boolean(savingWorkflow)}
              onValueChange={(value) => void updateHarness('default', value)}
            />
            <ModelPreferenceSelect key={generalHarness} label="Default harness" value={defaults.default?.model ?? ''} disabled={resettingHarnesses || Boolean(savingWorkflow)} discovery={models.choices[generalHarness]} load={(retry) => models.load(generalHarness, retry)} onSave={(model) => updateHarness('default', generalHarness, model, defaults.default?.permissions)} />
            <PermissionPreferenceSelect label="Default harness" harness={generalHarness} value={defaults.default?.permissions ?? ''} disabled={resettingHarnesses || Boolean(savingWorkflow)} onSave={(permissions) => updateHarness('default', generalHarness, defaults.default?.model ?? '', permissions)} />
            </div>
          </div>

          <div className={styles.overrideHeader}>
            <div>
              <h3>Task-specific defaults</h3>
              <p>Overrides only affect new work started by that action.</p>
            </div>
            <Button variant="ghost" size="small" disabled={customizedHarnessCount === 0 || resettingHarnesses || Boolean(savingWorkflow)} onClick={() => void resetAllHarnesses()}>
              Reset all
            </Button>
          </div>
          <div className={styles.preferenceList}>
            {taskWorkflows.map(({ workflow, label, icon }) => {
              const preference = defaults[workflow]
              const effectiveHarness = preference?.harness_type ?? generalHarness
              return (
                <div className={styles.preferenceRow} key={workflow}>
                  <span className={styles.taskIcon} aria-hidden="true">{icon}</span>
                  <div className={styles.preferenceCopy}>
                    <strong>{label}</strong>
                  </div>
                  <div className={styles.preferenceControls}>
                  <HarnessPreferenceSelect
                    workflow={workflow}
                    value={preference?.explicit ? effectiveHarness : 'inherit'}
                    effectiveHarness={effectiveHarness}
                    capabilities={capabilities}
                    saving={resettingHarnesses || Boolean(savingWorkflow)}
                    onValueChange={(value) => void updateHarness(workflow, value)}
                  />
                  <ModelPreferenceSelect key={`${effectiveHarness}:${Boolean(preference?.explicit)}`} label={label} value={preference?.model ?? (!preference?.explicit ? defaults.default?.model : '') ?? ''} inherited={!preference?.explicit} disabled={resettingHarnesses || Boolean(savingWorkflow)} discovery={models.choices[effectiveHarness]} load={(retry) => models.load(effectiveHarness, retry)} onSave={(model) => updateHarness(workflow, effectiveHarness, model, preference?.permissions)} />
                  <PermissionPreferenceSelect label={label} harness={effectiveHarness} value={preference?.permissions ?? (!preference?.explicit ? defaults.default?.permissions : '') ?? ''} inherited={!preference?.explicit} disabled={resettingHarnesses || Boolean(savingWorkflow)} onSave={(permissions) => updateHarness(workflow, effectiveHarness, preference?.model ?? '', permissions)} />
                  </div>
                </div>
              )
            })}
          </div>
        </section>

        <section className={styles.section} aria-labelledby="appearance-title">
          <header className={styles.sectionHeader}>
            <div className={styles.sectionIcon}>{theme === 'dark' ? <Moon aria-hidden="true" /> : <Sun aria-hidden="true" />}</div>
            <div>
              <h2 id="appearance-title">Appearance</h2>
              <p id="appearance-description">Saved in this browser and applied immediately.</p>
            </div>
          </header>
          <fieldset className={styles.themeOptions} aria-describedby="appearance-description">
            <legend className="sr-only">Color theme</legend>
            <ThemeCard value="dark" label="Dark" description="A charcoal workspace with light text." icon={<Moon />} selected={theme === 'dark'} onSelect={setTheme} />
            <ThemeCard value="light" label="Light" description="A bright workspace with dark text." icon={<Sun />} selected={theme === 'light'} onSelect={setTheme} />
          </fieldset>
        </section>

        <section className={styles.section} aria-labelledby="context-usage-title">
          <header className={styles.sectionHeader}>
            <div className={styles.sectionIcon}><Gauge aria-hidden="true" /></div>
            <div>
              <h2 id="context-usage-title">Context window</h2>
              <p id="context-usage-description">Highlight sessions as their context usage increases. Stored locally for this browser.</p>
            </div>
          </header>
          <ContextUsageSettings
            key={`${contextTokenThresholds.warning}:${contextTokenThresholds.danger}`}
            thresholds={contextTokenThresholds}
            onSave={setContextTokenThresholds}
          />
        </section>

        <header className={styles.subsectionHeader}>
          <span className={styles.eyebrow}>Automation</span>
          <h2>Prompt templates</h2>
          <p>Customize the instructions Holark gives agents for its built-in workflows.</p>
        </header>
        <div className={styles.templateList}>
          {templates.map((template) => {
            const draft = drafts[template.key] ?? template.value
            const changed = draft !== template.value
            const variables = template.variables ?? []
            return (
              <section className={styles.templatePanel} key={template.key} aria-labelledby={`${template.key}-title`}>
                <header className={styles.templateHeader}>
                  <div>
                    <h2 id={`${template.key}-title`}>{template.name}</h2>
                    <p>{template.use}</p>
                  </div>
                  <span className={template.overridden ? styles.overrideBadge : styles.defaultBadge}>{template.overridden ? 'Custom' : 'Default'}</span>
                </header>
                <div className={styles.variables} aria-label={`Variables for ${template.name}`}>
                  {variables.length === 0 ? <span>No variables</span> : variables.map((variable) => (
                    <span key={variable.name} title={variable.description}>{`{{${variable.name}}}`}</span>
                  ))}
                </div>
                <label className={styles.editorLabel}>
                  Prompt text
                  <textarea value={draft} rows={12} maxLength={65536} onChange={(event) => setDrafts((current) => ({ ...current, [template.key]: event.target.value }))} />
                </label>
                <div className={styles.actions}>
                  <Button icon={<RotateCcw />} disabled={savingKey === template.key || draft === template.default_value} onClick={() => void updateTemplate(template, template.default_value)}>Reset to default</Button>
                  <Button variant="primary" icon={<Save />} disabled={savingKey === template.key || !changed || draft.trim() === ''} onClick={() => void updateTemplate(template, draft)}>{savingKey === template.key ? 'Saving…' : 'Save'}</Button>
                </div>
              </section>
            )
          })}
        </div>
      </main>
    </div>
  )
}

const harnessTypes: HarnessType[] = ['codex', 'claude-code', 'opencode']
const emptyLaunchCommands: Record<HarnessType, string> = { codex: '', 'claude-code': '', opencode: '' }
const launchPlaceholders: Record<HarnessType, string> = { codex: 'codex', 'claude-code': 'claude', opencode: 'opencode' }

function LaunchCommandSettings({ onSaved }: { onSaved: () => Promise<unknown> }) {
  const [commands, setCommands] = useState(emptyLaunchCommands)
  const [saved, setSaved] = useState(emptyLaunchCommands)
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [open, setOpen] = useState(false)
  useEffect(() => {
    if (!open || loaded) return
    let active = true
    void api.agentLaunchCommands().then(({ commands }) => {
      if (!active) return
      setCommands(commands)
      setSaved(commands)
      setLoaded(true)
      setError('')
    }).catch((reason: unknown) => { if (active) setError(errorMessage(reason)) })
    return () => { active = false }
  }, [open, loaded])
  const save = async (event: FormEvent) => {
    event.preventDefault()
    setSaving(true)
    setError('')
    setMessage('')
    try {
      const changes = Object.fromEntries(harnessTypes
        .filter((kind) => commands[kind] !== saved[kind])
        .map((kind) => [kind, commands[kind]]))
      await api.updateAgentLaunchCommands(changes)
      setSaved(commands)
      setMessage('Launch commands saved.')
      await onSaved()
    } catch (reason) {
      setError(errorMessage(reason))
    } finally {
      setSaving(false)
    }
  }
  return <details className={styles.launchCommands} onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>Launch commands</summary>
    {open && <div>
      <p>Shared across repositories in this Holark home. Changes apply to subsequent launches; running sessions continue unchanged. Leave a field empty to use its standard command.</p>
      <form onSubmit={(event) => void save(event)}>
        {harnessTypes.map((kind) => <label className={styles.commandField} key={kind}>
          <span>{harnessDisplayLabel(kind)}</span>
          <input value={commands[kind]} placeholder={launchPlaceholders[kind]} disabled={!loaded || saving} maxLength={4096} spellCheck={false} autoComplete="off" onChange={(event) => { setCommands((current) => ({ ...current, [kind]: event.target.value })); setMessage('') }} />
        </label>)}
        <p>Use an absolute executable path or a command name on PATH. Arguments and quoted paths are supported, for example npx claude. Use a wrapper script for environment setup or multiple commands; shell expansion is not performed.</p>
        {error && <p className={styles.fieldError} role="alert">{error}</p>}
        {message && <p role="status">{message}</p>}
        <div className={styles.actions}><Button type="submit" disabled={!loaded || saving || harnessTypes.every((kind) => commands[kind] === saved[kind])}>{saving ? 'Saving…' : 'Save'}</Button></div>
      </form>
    </div>}
  </details>
}

function HarnessPreferenceSelect({ workflow, value, effectiveHarness, capabilities, saving, onValueChange }: {
  workflow: HarnessWorkflow
  value: PreferenceValue
  effectiveHarness: HarnessType
  capabilities: HarnessCapability[]
  saving: boolean
  onValueChange: (value: PreferenceValue) => void
}) {
  const inherited = value === 'inherit'
  const automated = workflow !== 'default'
  return (
    <Select.Root value={value} disabled={saving} onValueChange={(next) => onValueChange(next as PreferenceValue)}>
      <Select.Trigger className={styles.harnessTrigger} aria-label={workflow === 'default' ? 'Default harness' : `${workflowLabel(workflow)} harness`}>
        <span className={styles.triggerCopy}>
          <strong className={inherited ? styles.defaultValue : undefined}>{inherited ? 'Use default' : harnessDisplayLabel(effectiveHarness)}</strong>
          {capabilities.find((candidate) => candidate.type === effectiveHarness)?.warning && <small>Version warning</small>}
        </span>
        <Select.Icon asChild><ChevronDown aria-hidden="true" /></Select.Icon>
      </Select.Trigger>
      <Select.Portal>
        <Select.Content className={styles.harnessContent} position="popper" sideOffset={6} align="end" collisionPadding={10}>
          <Select.Viewport className={styles.harnessViewport}>
            {workflow !== 'default' && <>
              <Select.Item className={styles.harnessOption} value="inherit">
                <Select.ItemText>
                  <span className={styles.optionCopy}><strong className={styles.defaultValue}>Use default</strong></span>
                </Select.ItemText>
                <Select.ItemIndicator className={styles.harnessIndicator}><Check aria-hidden="true" /></Select.ItemIndicator>
              </Select.Item>
              <Select.Separator className={styles.harnessSeparator} />
            </>}
            {capabilities.map((capability) => {
              const harnessType = capability.type
              const available = Boolean(capability?.available && (!automated || capability.automated_workflows !== false))
              const detail = available
                ? harnessVersionDetail(capability)
                : capability?.available && automated ? 'Does not support automated work' : capability?.unavailable_reason || 'Unavailable'
              return (
                <Select.Item className={styles.harnessOption} key={harnessType} value={harnessType} disabled={!available}>
                  <Select.ItemText>
                    <span className={styles.optionCopy}><strong>{harnessDisplayLabel(harnessType)}</strong><small>{detail}</small></span>
                  </Select.ItemText>
                  <Select.ItemIndicator className={styles.harnessIndicator}><Check aria-hidden="true" /></Select.ItemIndicator>
                </Select.Item>
              )
            })}
          </Select.Viewport>
        </Select.Content>
      </Select.Portal>
    </Select.Root>
  )
}

function ThemeCard({ value, label, description, icon, selected, onSelect }: {
  value: ColorTheme
  label: string
  description: string
  icon: ReactNode
  selected: boolean
  onSelect: (theme: ColorTheme) => void
}) {
  return (
    <label className={`${styles.themeCard} ${selected ? styles.selectedTheme : ''}`}>
      <input type="radio" name="color-theme" value={value} checked={selected} onChange={() => onSelect(value)} />
      <span className={styles.themeIcon} aria-hidden="true">{icon}</span>
      <span className={styles.themeCopy}><strong>{label}</strong><span>{description}</span></span>
      <span className={styles.checkIndicator} aria-hidden="true">{selected && <Check />}</span>
    </label>
  )
}

function ContextUsageSettings({ thresholds, onSave }: {
  thresholds: ContextTokenThresholds
  onSave: (thresholds: ContextTokenThresholds) => void
}) {
  const initialWarning = thresholdInThousands(thresholds.warning)
  const initialDanger = thresholdInThousands(thresholds.danger)
  const [drafts, setDrafts] = useState(() => ({ warning: String(initialWarning), danger: String(initialDanger) }))
  const [scaleMaximum, setScaleMaximum] = useState(() => contextScaleFor(initialDanger))
  const [draggingThreshold, setDraggingThreshold] = useState<'warning' | 'danger' | null>(null)
  const [error, setError] = useState('')
  const changed = drafts.warning !== String(initialWarning) || drafts.danger !== String(initialDanger)
  const customized = thresholds.warning !== defaultContextTokenThresholds.warning || thresholds.danger !== defaultContextTokenThresholds.danger

  const warningDraft = Number(drafts.warning)
  const dangerDraft = Number(drafts.danger)
  const warningValue = Number.isFinite(warningDraft) ? Math.min(Math.max(warningDraft, 0), scaleMaximum) : initialWarning
  const dangerValue = Number.isFinite(dangerDraft) ? Math.min(Math.max(dangerDraft, 0), scaleMaximum) : initialDanger
  const warningPosition = warningValue / scaleMaximum * 100
  const dangerPosition = dangerValue / scaleMaximum * 100

  const updateDraft = (threshold: 'warning' | 'danger', value: string, growScale = true) => {
    setDrafts((current) => ({ ...current, [threshold]: value }))
    setError('')
    const numericValue = Number(value)
    if (growScale && Number.isFinite(numericValue) && numericValue >= scaleMaximum * contextScaleExpansionPoint) {
      setScaleMaximum(contextScaleFor(numericValue))
    }
  }

  const updateFromSlider = (threshold: 'warning' | 'danger', value: number) => {
    const constrainedValue = threshold === 'warning'
      ? Math.min(value, dangerValue - contextSliderStep)
      : Math.max(value, warningValue + contextSliderStep)
    updateDraft(threshold, String(constrainedValue), false)
    return constrainedValue
  }

  const finishDragging = (threshold: 'warning' | 'danger', value: number) => {
    setDraggingThreshold(null)
    const releasedValue = updateFromSlider(threshold, value)
    const highestValue = threshold === 'danger' ? releasedValue : dangerValue
    const previousMaximum = previousContextScale(scaleMaximum)
    const nextMaximum = releasedValue >= scaleMaximum * contextScaleExpansionPoint
      ? nextContextScale(scaleMaximum)
      : previousMaximum < scaleMaximum && highestValue < previousMaximum * contextScaleExpansionPoint
        ? previousMaximum
        : scaleMaximum
    if (nextMaximum !== scaleMaximum) {
      const releasedMaximum = scaleMaximum
      window.requestAnimationFrame(() => {
        setScaleMaximum((current) => current === releasedMaximum ? nextMaximum : current)
      })
    }
  }

  const save = (event: FormEvent) => {
    event.preventDefault()
    const warning = Number(drafts.warning)
    const danger = Number(drafts.danger)
    if (!Number.isSafeInteger(warning) || warning < 0 || warning > 1_000 || !Number.isSafeInteger(danger) || danger > 1_000 || danger <= warning) {
      setError('Enter whole values from 0k to 1,000k, with the critical threshold higher than the warning threshold.')
      return
    }
    setError('')
    onSave({ warning: warning * 1_000, danger: danger * 1_000 })
  }

  const reset = () => {
    setError('')
    const warning = thresholdInThousands(defaultContextTokenThresholds.warning)
    const danger = thresholdInThousands(defaultContextTokenThresholds.danger)
    setDrafts({ warning: String(warning), danger: String(danger) })
    setScaleMaximum(contextScaleFor(danger))
    onSave(defaultContextTokenThresholds)
  }

  return (
    <form className={styles.thresholdForm} aria-describedby="context-usage-description" onSubmit={save}>
      <div className={`${styles.thresholdScale} ${draggingThreshold ? styles.draggingScale : ''}`}>
        <div className={styles.scaleEndpoints} aria-hidden="true">
          <span>0</span>
          <span>{contextScaleLabel(scaleMaximum)}</span>
        </div>
        <div className={styles.scaleSlider}>
          <div className={styles.scaleRail} aria-hidden="true">
            <span className={styles.normalRange} style={{ width: `${warningPosition}%` }} />
            <span className={styles.warningRange} style={{ left: `${warningPosition}%`, width: `${Math.max(0, dangerPosition - warningPosition)}%` }} />
            <span className={styles.dangerRange} style={{ left: `${dangerPosition}%` }} />
            {scaleMaximum < 1_000 && <span className={styles.expansionRange} />}
          </div>
          <span className={`${styles.scaleThumb} ${styles.warningThumb}`} style={{ left: `${warningPosition}%` }} aria-hidden="true" />
          <span className={`${styles.scaleThumb} ${styles.dangerThumb}`} style={{ left: `${dangerPosition}%` }} aria-hidden="true" />
          <input
            className={styles.scaleInput}
            type="range"
            min="0"
            max={scaleMaximum}
            step={contextSliderStep}
            value={warningValue}
            aria-label="Warning threshold"
            aria-valuetext={`${warningValue}k tokens`}
            onChange={(event) => updateFromSlider('warning', Number(event.target.value))}
            onPointerDown={() => setDraggingThreshold('warning')}
            onPointerUp={(event) => finishDragging('warning', Number(event.currentTarget.value))}
            onKeyUp={(event) => finishDragging('warning', Number(event.currentTarget.value))}
            onBlur={() => setDraggingThreshold(null)}
          />
          <input
            className={styles.scaleInput}
            type="range"
            min="0"
            max={scaleMaximum}
            step={contextSliderStep}
            value={dangerValue}
            aria-label="Critical threshold"
            aria-valuetext={`${dangerValue}k tokens`}
            onChange={(event) => updateFromSlider('danger', Number(event.target.value))}
            onPointerDown={() => setDraggingThreshold('danger')}
            onPointerUp={(event) => finishDragging('danger', Number(event.currentTarget.value))}
            onKeyUp={(event) => finishDragging('danger', Number(event.currentTarget.value))}
            onBlur={() => setDraggingThreshold(null)}
          />
        </div>
        <div className={styles.scaleLegend}>
          <label>
            <i className={styles.warningDot} aria-hidden="true" />
            <span>Warning at</span>
            <span className={styles.legendNumber}>
              <input type="number" min="0" max="1000" step="1" inputMode="numeric" value={drafts.warning} aria-label="Warning threshold in thousands of tokens" onChange={(event) => updateDraft('warning', event.target.value)} />
              <span>k</span>
            </span>
          </label>
          <label>
            <i className={styles.dangerDot} aria-hidden="true" />
            <span>Critical at</span>
            <span className={styles.legendNumber}>
              <input type="number" min="1" max="1000" step="1" inputMode="numeric" value={drafts.danger} aria-label="Critical threshold in thousands of tokens" onChange={(event) => updateDraft('danger', event.target.value)} />
              <span>k</span>
            </span>
          </label>
        </div>
      </div>
      {error && <p className={styles.fieldError} role="alert">{error}</p>}
      <div className={styles.thresholdActions}>
        <Button icon={<RotateCcw />} disabled={!customized && !changed} onClick={reset}>Reset defaults</Button>
        <Button type="submit" variant="primary" icon={<Save />} disabled={!changed}>Save changes</Button>
      </div>
    </form>
  )
}

function workflowLabel(workflow: HarnessWorkflow) {
  if (workflow === 'default') return 'Default harness'
  return taskWorkflows.find((candidate) => candidate.workflow === workflow)?.label ?? 'Agent workflow'
}

function errorMessage(value: unknown) {
  const error = value as Error
  return error?.message || 'Request failed.'
}
