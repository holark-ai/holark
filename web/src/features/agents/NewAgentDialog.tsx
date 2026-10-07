import { useAgentCapabilities } from '../../data/useAgentCapabilities'
import * as Select from '@radix-ui/react-select'
import { Check, ChevronDown, Pencil } from 'lucide-react'
import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button } from '../../components/Button'
import { Dialog } from '../../components/Dialog'
import { api } from '../../data/api'
import { defaultHarnessType, harnessDisplayLabel, harnessTypes, hasAvailableHarness } from '../../data/harness'
import type { ApiProject, HarnessCapabilities, HarnessCapability, HarnessType, HarnessWorkflow, Holon } from '../../data/types'
import { useOptionalHolonStore } from '../project/holonStoreContext'
import { BranchCombobox } from '../repository/BranchCombobox'
import { useRepositoryBranches } from '../repository/useRepositoryBranches'
import styles from './NewAgentDialog.module.css'
import { limitHolonTitle } from './holonTitle'

type Props = {
  open: boolean
  project: ApiProject
  capabilities?: HarnessCapabilities
  onClose: () => void
  initialPrompt?: string
  initialContext?: string
  issueId?: string
  initialBranch?: string
  title?: string
  submitLabel?: string
  harnessWorkflow?: HarnessWorkflow
  allowTerminal?: boolean
  draftScope?: string
  nameHint?: string
  onSubmit?: (prompt: string, holonTitle?: string, harnessType?: HarnessType, startupMode?: 'terminal') => Promise<Holon>
}

type StartupChoice = HarnessType | 'terminal'

function startupLabelFor(choice: StartupChoice) {
  return choice === 'terminal' ? 'Terminal' : harnessDisplayLabel(choice)
}

function dropdownVersion(capability: HarnessCapability) {
  if (capability.support_status === 'unknown') return 'Version unknown'
  return capability.version ? `v${capability.version}` : 'Available'
}

type StartupPhase = 'idle' | 'creating'

export function NewAgentDialog({ open, project, capabilities, onClose, initialPrompt = '', initialContext, issueId, initialBranch, title, submitLabel, onSubmit, allowTerminal = !onSubmit, draftScope = project.id, nameHint, harnessWorkflow = 'default' }: Props) {
  const navigate = useNavigate()
  const isMac = /Mac|iPhone|iPad|iPod/i.test(navigator.platform)
  const holonTitleId = useId()
  const holonTitleTooltipId = useId()
  const holonTitleRef = useRef<HTMLInputElement>(null)
  const nameButtonRef = useRef<HTMLButtonElement>(null)
  const restoreNameFocusRef = useRef(false)
  const holonTitleBeforeEditRef = useRef('')
  const holonStore = useOptionalHolonStore()
  const promptId = useId()
  const promptRef = useRef<HTMLTextAreaElement>(null)
  const wasOpenRef = useRef(false)
  const harnessSelectionTouchedRef = useRef(false)
  const draftKey = useMemo(() => agentDraftKey(draftScope), [draftScope])
  const dialogGenerationRef = useRef(0)
  const capabilityState = useAgentCapabilities(open && !capabilities)
  const capabilitiesLoading = !capabilities && capabilityState.loading
  const localCapabilities: HarnessCapabilities = capabilities ?? capabilityState.data ?? []
  const selectableHarnessTypes: StartupChoice[] = allowTerminal ? [...harnessTypes, 'terminal'] : harnessTypes
  const defaultHarness = localCapabilities.harness_defaults?.[harnessWorkflow]?.harness_type ?? defaultHarnessType(localCapabilities)
  const [harnessType, setHarnessType] = useState<StartupChoice>(defaultHarness)
  const [holonTitle, setSessionTitle] = useState('')
  const [editingName, setEditingName] = useState(false)
  const [prompt, setPrompt] = useState('')
  const [branch, setBranch] = useState(initialBranch || project.default_branch)
  const [startupPhase, setStartupPhase] = useState<StartupPhase>('idle')
  const [error, setError] = useState('')
  const terminalSelected = allowTerminal && harnessType === 'terminal'
  const selectedCapability = localCapabilities.find((candidate) => candidate.type === harnessType)
  const selectedHarnessAvailable = terminalSelected || (harnessType !== 'terminal' && hasAvailableHarness(localCapabilities, harnessType))
  const submitting = startupPhase !== 'idle'
  const canSubmit = selectedHarnessAvailable && !submitting
  const branchState = useRepositoryBranches(project.id, project.default_branch, open && !onSubmit)
  const startupLabel = {
    idle: submitLabel ?? (onSubmit ? 'Start agent' : 'Create Holon'),
    creating: 'Creating holon...',
  }[startupPhase]

  useEffect(() => {
    if (editingName) {
      holonTitleRef.current?.focus()
      holonTitleRef.current?.select()
    } else if (restoreNameFocusRef.current) {
      restoreNameFocusRef.current = false
      nameButtonRef.current?.focus()
    }
  }, [editingName])
  useEffect(() => {
    if (open && !harnessSelectionTouchedRef.current) setHarnessType(defaultHarness)
  }, [defaultHarness, open])

  const beginNameEdit = () => {
    holonTitleBeforeEditRef.current = holonTitle
    setEditingName(true)
  }

  const finishNameEdit = () => {
    setSessionTitle(holonTitle.trim())
    setEditingName(false)
  }

  const cancelNameEdit = () => {
    setSessionTitle(holonTitleBeforeEditRef.current)
    setEditingName(false)
  }

  const resetForm = useCallback((nextPrompt = '', nextBranch = initialBranch || project.default_branch) => {
    setSessionTitle('')
    setEditingName(false)
    setPrompt(nextPrompt)
    setBranch(nextBranch)
    harnessSelectionTouchedRef.current = false
    setHarnessType(defaultHarness)
    setStartupPhase('idle')
    setError('')
  }, [defaultHarness, initialBranch, project.default_branch])

  useEffect(() => {
    if (open && !wasOpenRef.current) {
      const selectedBranch = initialBranch || project.default_branch
      resetForm(initialPrompt || readAgentDraft(draftKey), selectedBranch)
      ++dialogGenerationRef.current
    } else if (!open && wasOpenRef.current) {
      ++dialogGenerationRef.current
      setStartupPhase('idle')
      setError('')
    }
    wasOpenRef.current = open
  }, [draftKey, initialBranch, initialPrompt, onSubmit, open, project.default_branch, resetForm, submitting])

  useEffect(() => () => {
    wasOpenRef.current = false
    ++dialogGenerationRef.current
  }, [])

  const changeBranch = (nextBranch: string) => {
    if (nextBranch === branch || startupPhase === 'creating') return
    setStartupPhase('idle')
    setBranch(nextBranch)
    setError('')
    ++dialogGenerationRef.current
  }

  const updatePrompt = (nextPrompt: string) => {
    setPrompt(nextPrompt)
    if (!initialPrompt) writeAgentDraft(draftKey, nextPrompt)
  }

  const clearPrompt = () => {
    updatePrompt('')
    promptRef.current?.focus()
  }

  const close = () => {
    if (startupPhase === 'creating') return
    setStartupPhase('idle')
    ++dialogGenerationRef.current
    onClose()
  }

  const submit = async () => {
    if (!canSubmit) return
    const generation = dialogGenerationRef.current
    const task = [prompt.trim(), initialContext ? `Attached context:\n${initialContext}` : ''].filter(Boolean).join('\n\n')
    setStartupPhase('creating')
    setError('')
    try {
      const requestedTitle = holonTitle.trim()
      let holon: Holon
      if (onSubmit) {
        holon = harnessType === 'terminal'
          ? await onSubmit(task, requestedTitle || undefined, undefined, 'terminal')
          : await onSubmit(task, requestedTitle || undefined, harnessType)
      } else {
        holon = await api.createHolon(task, {
          title: requestedTitle || undefined,
          issueId,
          baseBranch: branch,
          respondAsync: true,
          harnessType: harnessType === 'terminal' ? undefined : harnessType,
          startupMode: terminalSelected ? 'terminal' : undefined,
        })
      }
      holonStore?.updateHolon(holon.id, holon)
      void holonStore?.refresh()
      if (!initialPrompt) removeAgentDraft(draftKey)
      ++dialogGenerationRef.current
      resetForm()
      onClose()
      navigate(`/holons/${holon.id}`, { state: { suggestFullscreen: true } })
    } catch (value) {
      if (generation !== dialogGenerationRef.current) return
      setError(value instanceof Error ? value.message : 'The Holon could not be created.')
      setStartupPhase('idle')
    }
  }

  const startAgent = () => {
    if (!canSubmit) return
    void submit()
  }

  return (
    <Dialog
      open={open}
      onKeyDownCapture={(event) => {
        if (event.defaultPrevented || event.nativeEvent.isComposing || event.repeat) return
        if (event.target instanceof Element && event.target.closest('[data-holark-dialog-nested-layer]')) return
        // Option changes event.key on macOS, so match the physical H key.
        if (event.code === 'KeyH' && event.shiftKey && event.altKey && !event.metaKey && !event.ctrlKey) {
          event.preventDefault()
          event.stopPropagation()
          if (!submitting) {
            harnessSelectionTouchedRef.current = true
            setHarnessType((current) => selectableHarnessTypes[(selectableHarnessTypes.indexOf(current) + 1) % selectableHarnessTypes.length])
          }
          return
        }
        if (event.key !== 'Enter') return
        if (event.altKey || event.shiftKey || (isMac ? !event.metaKey || event.ctrlKey : !event.ctrlKey || event.metaKey)) return
        event.preventDefault()
        event.stopPropagation()
        startAgent()
      }}
      title={holonTitle.trim() || title || (onSubmit ? 'New agent' : 'New Holon')}
      titleControl={editingName ? (
        <label className={styles.nameEditor} htmlFor={holonTitleId}>
          <span className="sr-only">Holon name (optional)</span>
          <input
            id={holonTitleId}
            ref={holonTitleRef}
            className={styles.nameInput}
            data-holark-dialog-escape-handler=""
            disabled={submitting}
            value={holonTitle}
            onChange={(event) => setSessionTitle(limitHolonTitle(event.target.value))}
            onBlur={finishNameEdit}
            onKeyDown={(event) => {
              if (event.key === 'Enter' && !event.metaKey && !event.ctrlKey) {
                event.preventDefault()
                restoreNameFocusRef.current = true
                finishNameEdit()
              } else if (event.key === 'Escape') {
                event.preventDefault()
                event.stopPropagation()
                restoreNameFocusRef.current = true
                cancelNameEdit()
              }
            }}
            placeholder="Holon name"
          />
        </label>
      ) : undefined}
      headerAction={!editingName ? (
        <span className={styles.nameAction}>
          <Button
            ref={nameButtonRef}
            className={styles.nameButton}
            size="small"
            variant="ghost"
            aria-label={holonTitle ? 'Edit Holon name' : 'Set a custom Holon name'}
            aria-describedby={holonTitleTooltipId}
            disabled={submitting}
            onClick={beginNameEdit}
            icon={<Pencil aria-hidden="true" />}
          />
          <span className={styles.nameTooltip} id={holonTitleTooltipId} role="tooltip">
            {holonTitle ? 'Edit the Holon name.' : nameHint ?? (terminalSelected && !prompt.trim() ? 'Set a custom name. Without a task, the name will be Holon.' : 'Set a custom name. Otherwise, one will be generated automatically.')}
          </span>
        </span>
      ) : undefined}
      onClose={close}
      initialFocusRef={promptRef}
      footer={(
        <>
      {initialContext && <p className={styles.attachedContext}><strong>Attached context</strong><br />{initialContext}</p>}
          <Button disabled={startupPhase === 'creating'} onClick={close}>Cancel</Button>
          <Button variant="primary" aria-keyshortcuts={isMac ? 'Meta+Enter' : 'Control+Enter'} disabled={!canSubmit} onClick={startAgent}>
            {startupLabel}
            {!submitting && <kbd className={`${styles.submitShortcut} ${styles.submitFaded}`} title={`Keyboard shortcut: ${isMac ? '⌘' : 'Ctrl'} + Enter`} aria-hidden="true">{isMac ? '⌘ ↵' : 'Ctrl ↵'}</kbd>}
          </Button>
        </>
      )}
    >
      <div className={styles.form}>
        <div className={styles.field}>
          <div className={styles.taskHeader}>
            <label className={styles.taskLabel} htmlFor={promptId}>{terminalSelected ? 'Task (optional)' : 'Task'}</label>
            <Button className={styles.clearPrompt} disabled={!prompt || submitting} size="small" variant="ghost" onClick={clearPrompt}>Clear</Button>
          </div>
          <textarea id={promptId} ref={promptRef} value={prompt} rows={6} maxLength={65536} onChange={(event) => updatePrompt(event.target.value)} placeholder={terminalSelected ? 'Describe the work you plan to do…' : `Describe what ${startupLabelFor(harnessType)} should do…`} />
        </div>
        {terminalSelected && <p className={styles.notice}>The task describes your work. It is not executed in the terminal.</p>}
        <div className={styles.details}>
          {onSubmit ? (
            <div className={styles.detailTile}>
              <span className={styles.detailLabel}>Branch</span>
              <span className={styles.detailValue} title={initialBranch || project.default_branch}>{initialBranch || project.default_branch}</span>
            </div>
          ) : (
            <BranchCombobox
              branches={branchState.branches}
              value={branch}
              defaultBranch={project.default_branch}
              loading={branchState.loading}
              refreshing={branchState.refreshing}
              error={branchState.error}
              disabled={startupPhase === 'creating'}
              variant="detail"
              onOpen={() => void branchState.refresh()}
              onValueChange={changeBranch}
            />
          )}
          <HarnessSelect
            capabilities={localCapabilities}
            value={harnessType}
            isMac={isMac}
            options={selectableHarnessTypes}
            onValueChange={(nextHarness) => {
              harnessSelectionTouchedRef.current = true
              setHarnessType(nextHarness)
            }}
          />
        </div>
        {!terminalSelected && capabilitiesLoading && <p className={styles.notice}>Loading agent harnesses…</p>}
        {!terminalSelected && !capabilities && capabilityState.error && <p className={styles.error} role="alert">Couldn’t load available agents. <Button size="small" variant="ghost" onClick={() => void capabilityState.load().catch(() => {})}>Retry</Button></p>}
        {!capabilitiesLoading && !selectedHarnessAvailable && <p className={styles.notice}>
          {selectedCapability?.unavailable_reason || `${startupLabelFor(harnessType)} is unavailable`}. Choose an available default in <Link to="/settings">Settings</Link>.
        </p>}
        {error && <div className={styles.error} role="alert">{error}</div>}
      </div>
    </Dialog>
  )
}

function agentDraftKey(projectId: string) {
  return `holark:new-agent-draft:${projectId}`
}

function readAgentDraft(key: string) {
  try {
    const stored = window.localStorage.getItem(key)
    if (!stored) return ''
    const parsed = JSON.parse(stored) as { prompt?: unknown }
    return typeof parsed.prompt === 'string' ? parsed.prompt : ''
  } catch {
    return ''
  }
}

function writeAgentDraft(key: string, prompt: string) {
  try {
    window.localStorage.setItem(key, JSON.stringify({ prompt }))
  } catch {
    // Draft restore is optional browser state.
  }
}

function removeAgentDraft(key: string) {
  try {
    window.localStorage.removeItem(key)
  } catch {
    // Draft restore is optional browser state.
  }
}

function HarnessSelect({ value, options, capabilities, isMac, onValueChange }: { value: StartupChoice; options: StartupChoice[]; capabilities: HarnessCapabilities; isMac: boolean; onValueChange: (value: StartupChoice) => void }) {
  const shortcutLabel = isMac ? 'Shift + Option + H' : 'Shift + Alt + H'
  const triggerRef = useRef<HTMLButtonElement>(null)
  const [open, setOpen] = useState(false)
  const [portalContainer, setPortalContainer] = useState<HTMLElement | null>(null)

  const changeOpen = (nextOpen: boolean) => {
    if (nextOpen) setPortalContainer(triggerRef.current?.closest<HTMLElement>('[role="dialog"]') ?? null)
    setOpen(nextOpen)
  }

  return (
    <Select.Root open={open} value={value} onOpenChange={changeOpen} onValueChange={(nextValue) => onValueChange(nextValue as StartupChoice)}>
      <Select.Trigger ref={triggerRef} className={styles.harnessTrigger} aria-label="Start with" aria-keyshortcuts="Shift+Alt+H" title={`Cycle startup choice: ${shortcutLabel}`}>
        <span className={styles.detailCopy}>
          <span className={styles.harnessLabelRow}>
            <span className={styles.detailLabel}>Start with</span>
            <kbd className={styles.submitShortcut} aria-hidden="true">{isMac ? '⇧ ⌥ H' : '⇧ Alt H'}</kbd>
          </span>
          <Select.Value />
        </span>
        <Select.Icon asChild><ChevronDown className={styles.harnessChevron} aria-hidden="true" /></Select.Icon>
      </Select.Trigger>
      <Select.Portal container={portalContainer ?? undefined}>
        <Select.Content
          className={styles.harnessContent}
          position="popper"
          sideOffset={6}
          align="start"
          collisionPadding={8}
          data-holark-dialog-nested-layer=""
        >
          <Select.Viewport className={styles.harnessViewport}>
            {options.map((option) => {
              const capability = capabilities.find((candidate) => candidate.type === option)
              return <Select.Item className={styles.harnessOption} key={option} value={option}>
                <Select.ItemText>{startupLabelFor(option)}</Select.ItemText>
                {option !== 'terminal' && <small className={styles.harnessVersion}>
                  {capability?.available
                    ? dropdownVersion(capability)
                    : <>Unavailable <span className={styles.unavailableFace} aria-hidden="true">:(</span></>}
                </small>}
                <Select.ItemIndicator className={styles.harnessIndicator}><Check aria-hidden="true" /></Select.ItemIndicator>
              </Select.Item>
            })}
          </Select.Viewport>
        </Select.Content>
      </Select.Portal>
    </Select.Root>
  )
}
