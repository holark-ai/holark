package claudecode

import (
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

// Projection contains public state derived from Claude observation. Process
// lifecycle remains owned by the terminal domain and node-local TerminalHost.
type Projection struct {
	Activity        protocol.AgentActivity
	ActivityChanged bool
	ContextChanged  bool
	ContextTokens   *int64
	Metadata        bool
	ResumeTarget    string
	RolloutPath     string
	InputChanged    bool
	InputState      protocol.InputState
}

type interactionKind uint8

const (
	interactionPermission interactionKind = iota + 1
	interactionUser
)

type interaction struct {
	key           string
	kind          interactionKind
	typed         bool
	pending       bool
	sequence      uint64
	sessionID     string
	promptID      string
	agentID       string
	toolName      string
	toolUseID     string
	elicitationID string
}

type Reducer struct {
	activity         protocol.AgentActivity
	workActivity     protocol.AgentActivity
	activeSessionID  string
	inputState       protocol.InputState
	interactions     map[string]*interaction
	sequence         uint64
	presence         string
	stopAt           time.Time
	settled          bool
	hookSuccess      bool
	hookContinuation bool
	failed           bool
	contextTokens    *int64
}

func NewReducer(inputState protocol.InputState) *Reducer {
	if inputState == "" {
		inputState = protocol.InputNone
	}
	return &Reducer{
		inputState:   inputState,
		interactions: make(map[string]*interaction),
	}
}

func (reducer *Reducer) Apply(event NormalizedEvent) (projection Projection) {
	if reducer == nil {
		return Projection{}
	}
	defer func() { reducer.projectActivity(&projection) }()
	if reducer.activeSessionID != "" && event.SessionID != reducer.activeSessionID && event.Event != "SessionStart" {
		return projection
	}

	switch event.Event {
	case "SessionStart":
		if event.AgentID != "" {
			return projection
		}
		reducer.resetSettlement()
		reducer.presence = ""
		reducer.failed = false
		reducer.workActivity = protocol.ActivityIdle
		reducer.activeSessionID = event.SessionID
		reducer.contextTokens = nil
		reducer.clearInteractions()
		projection.Metadata = true
		projection.ResumeTarget = event.SessionID
		projection.RolloutPath = event.TranscriptPath
		// Keep inherited completion until this launch's presence is reconciled.
		if reducer.inputState != protocol.InputTaskComplete {
			reducer.setDerivedInput(&projection)
		}

	case "SessionEnd", "StopFailure":
		reducer.resetSettlement()
		reducer.failed = event.Event == "StopFailure"
		reducer.workActivity = protocol.ActivityIdle
		if event.Event == "StopFailure" {
			reducer.workActivity = protocol.ActivityFailed
		}
		reducer.clearInteractions()
		reducer.setInput(protocol.InputNone, &projection)

	case "UserPromptSubmit":
		if event.AgentID != "" {
			return projection
		}
		reducer.resetSettlement()
		reducer.failed = false
		reducer.workActivity = protocol.ActivityWorking
		// A new main-agent prompt supersedes stale attention and correlation
		// context from the previous turn, including subagent prompts.
		reducer.clearInteractions()
		reducer.setInput(protocol.InputNone, &projection)

	case "PermissionRequest":
		kind := interactionPermission
		if event.ToolName == "AskUserQuestion" {
			// Claude presents AskUserQuestion through its permission pipeline,
			// but the user is answering a question rather than granting a tool
			// permission.
			kind = interactionUser
		}
		reducer.openTyped(event, kind)
		reducer.setDerivedInput(&projection)

	case "PermissionDenied":
		reducer.resolveTyped(event, interactionKindsForClose(event)...)
		reducer.resolveFallbacks(event)
		reducer.setDerivedInput(&projection)

	case "PreToolUse":
		if event.AgentID == "" {
			reducer.resetSettlement()
		}
		reducer.workActivity = protocol.ActivityWorking
		if event.ToolName == "AskUserQuestion" {
			reducer.openTyped(event, interactionUser)
		}
		reducer.setDerivedInput(&projection)

	case "PostToolUse", "PostToolUseFailure":
		if event.AgentID == "" {
			reducer.resetSettlement()
		}
		reducer.workActivity = protocol.ActivityWorking
		if event.Interrupted {
			reducer.workActivity = protocol.ActivityIdle
		}
		reducer.resolveTyped(event, interactionKindsForClose(event)...)
		reducer.resolveFallbacks(event)
		reducer.setDerivedInput(&projection)

	case "Elicitation":
		reducer.openTyped(event, interactionUser)
		reducer.setDerivedInput(&projection)

	case "ElicitationResult":
		reducer.resolveTyped(event, interactionUser)
		reducer.resolveFallbacks(event)
		reducer.setDerivedInput(&projection)

	case "Notification":
		reducer.applyNotification(event)
		reducer.setDerivedInput(&projection)

	case "Stop":
		// A Stop from a subagent can never complete the Holark session.
		if event.AgentID != "" {
			return projection
		}
		reducer.resetSettlement()
		reducer.failed = false
		reducer.workActivity = protocol.ActivityWorking
		// Completion belongs to the main-agent turn. Background tasks and
		// scheduled jobs can outlive it; the transcript still must verify Stop.
		reducer.stopAt = time.UnixMilli(event.SentAt)

	case "SubagentStop":
		reducer.resolveAgent(event.AgentID)
		reducer.setDerivedInput(&projection)

	case "CwdChanged":
		// Working-directory changes are diagnostics, not input state.
	}
	return projection
}

// ApplyAttentionAction records that terminal input was successfully delivered
// for the prompt currently demanding attention. Claude's native TUI does not
// emit a documented close hook for every manual denial or cancellation, so
// this clears only the attention projection. It does not infer the decision or
// claim task completion, and it retains typed turn context so a delayed generic
// notification cannot reopen or misclassify the handled prompt.
func (reducer *Reducer) ApplyAttentionAction(action protocol.TerminalAttentionAction) Projection {
	projection := Projection{}
	if reducer == nil || reducer.activeSessionID == "" || !validAttentionAction(action) {
		return projection
	}
	kind := interactionPermission
	switch reducer.inputState {
	case protocol.InputUserRequired:
		kind = interactionUser
	case protocol.InputPermissionRequired:
	default:
		return projection
	}
	target := reducer.latestInteraction(func(candidate *interaction) bool {
		return candidate.pending && candidate.kind == kind
	})
	if target == nil {
		return projection
	}
	target.pending = false
	reducer.setDerivedInput(&projection)
	reducer.workActivity = protocol.ActivityUnknown
	reducer.projectActivity(&projection)
	return projection
}

func (reducer *Reducer) openTyped(event NormalizedEvent, kind interactionKind) {
	if event.ToolUseID == "" && event.ElicitationID == "" {
		if existing := reducer.latestTyped(event, kind); existing != nil {
			if existing.pending || (event.ToolName == "AskUserQuestion" && existing.toolUseID != "") {
				mergeInteractionIdentity(existing, event)
				return
			}
		}
	}
	key := typedInteractionKey(event, kind)
	if existing := reducer.interactions[key]; existing != nil {
		mergeInteractionIdentity(existing, event)
		if !existing.pending {
			reducer.sequence++
			existing.sequence = reducer.sequence
			existing.pending = true
		}
		return
	}
	reducer.sequence++
	reducer.interactions[key] = &interaction{
		key: key, kind: kind, typed: true, pending: true, sequence: reducer.sequence,
		sessionID: event.SessionID, promptID: event.PromptID, agentID: event.AgentID,
		toolName: event.ToolName, toolUseID: event.ToolUseID, elicitationID: event.ElicitationID,
	}
}

func (reducer *Reducer) applyNotification(event NormalizedEvent) {
	kind := notificationInteractionKind(event.NotificationType)
	if kind == 0 {
		if clearsAttentionNotification(event.NotificationType) {
			reducer.resolveFallbacks(event)
		}
		return
	}
	// Notification(permission_prompt) is a delayed, generic hint. A typed
	// question, elicitation, or permission in the same turn is authoritative,
	// including after the user has already handled it.
	if reducer.latestTypedForNotification(event) != nil {
		return
	}
	key := "notification:" + interactionKindName(kind) + ":" + interactionScope(event)
	if existing := reducer.interactions[key]; existing != nil {
		return
	}
	reducer.sequence++
	reducer.interactions[key] = &interaction{
		key: key, kind: kind, pending: true, sequence: reducer.sequence,
		sessionID: event.SessionID, promptID: event.PromptID, agentID: event.AgentID,
	}
}

func (reducer *Reducer) resolveTyped(event NormalizedEvent, kinds ...interactionKind) {
	if len(kinds) == 0 {
		return
	}
	allowed := make(map[interactionKind]bool, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = true
	}
	var exact *interaction
	if event.ToolUseID != "" || event.ElicitationID != "" {
		exact = reducer.latestInteraction(func(candidate *interaction) bool {
			if !candidate.typed || !allowed[candidate.kind] {
				return false
			}
			return event.ToolUseID != "" && candidate.toolUseID == event.ToolUseID ||
				event.ElicitationID != "" && candidate.elicitationID == event.ElicitationID
		})
	}
	if exact == nil {
		exact = reducer.latestInteraction(func(candidate *interaction) bool {
			return candidate.typed && allowed[candidate.kind] && sameStructuredScope(candidate, event)
		})
	}
	if exact != nil {
		exact.pending = false
	}
}

func (reducer *Reducer) resolveFallbacks(event NormalizedEvent) {
	for _, candidate := range reducer.interactions {
		if candidate.typed || !candidate.pending || candidate.sessionID != event.SessionID {
			continue
		}
		if event.PromptID != "" && candidate.promptID != "" && candidate.promptID != event.PromptID {
			continue
		}
		if event.AgentID != "" && candidate.agentID != event.AgentID {
			continue
		}
		candidate.pending = false
	}
}

func (reducer *Reducer) resolveAgent(agentID string) {
	if agentID == "" {
		return
	}
	for _, candidate := range reducer.interactions {
		if candidate.agentID == agentID {
			candidate.pending = false
		}
	}
}

func (reducer *Reducer) latestTyped(event NormalizedEvent, kind interactionKind) *interaction {
	return reducer.latestInteraction(func(candidate *interaction) bool {
		return candidate.typed && candidate.kind == kind && sameStructuredScope(candidate, event)
	})
}

func (reducer *Reducer) latestTypedForNotification(event NormalizedEvent) *interaction {
	return reducer.latestInteraction(func(candidate *interaction) bool {
		if !candidate.typed || candidate.sessionID != event.SessionID {
			return false
		}
		if event.PromptID != "" && candidate.promptID != "" && candidate.promptID != event.PromptID {
			return false
		}
		if event.AgentID != "" && candidate.agentID != event.AgentID {
			return false
		}
		return true
	})
}

func (reducer *Reducer) latestInteraction(matches func(*interaction) bool) *interaction {
	var latest *interaction
	for _, candidate := range reducer.interactions {
		if matches(candidate) && (latest == nil || candidate.sequence > latest.sequence) {
			latest = candidate
		}
	}
	return latest
}

func (reducer *Reducer) clearInteractions() {
	clear(reducer.interactions)
}

func (reducer *Reducer) setDerivedInput(projection *Projection) {
	state := protocol.InputNone
	for _, candidate := range reducer.interactions {
		if candidate.pending && candidate.kind == interactionUser {
			state = protocol.InputUserRequired
			break
		}
		if candidate.pending && candidate.kind == interactionPermission {
			state = protocol.InputPermissionRequired
		}
	}
	// Explicit interactions take priority, but resolving them reveals the
	// verified turn completion until main-agent work resumes.
	if state == protocol.InputNone && reducer.settled {
		state = protocol.InputTaskComplete
	}
	reducer.setInput(state, projection)
}

func (reducer *Reducer) setInput(state protocol.InputState, projection *Projection) {
	if reducer.inputState == state {
		return
	}
	reducer.inputState = state
	projection.InputChanged = true
	projection.InputState = state
}

func typedInteractionKey(event NormalizedEvent, kind interactionKind) string {
	if event.ToolUseID != "" {
		return "typed:tool:" + event.ToolUseID
	}
	if event.ElicitationID != "" {
		return "typed:elicitation:" + event.ElicitationID
	}
	return "typed:" + interactionKindName(kind) + ":" + interactionScope(event)
}

func interactionScope(event NormalizedEvent) string {
	scope := "session:" + event.SessionID
	if event.PromptID != "" {
		scope = "prompt:" + event.PromptID
	}
	if event.AgentID != "" {
		scope += ":agent:" + event.AgentID
	} else {
		scope += ":main"
	}
	return scope
}

func sameStructuredScope(candidate *interaction, event NormalizedEvent) bool {
	if candidate.sessionID != event.SessionID || candidate.agentID != event.AgentID {
		return false
	}
	if event.PromptID != "" && candidate.promptID != "" && candidate.promptID != event.PromptID {
		return false
	}
	return true
}

func mergeInteractionIdentity(candidate *interaction, event NormalizedEvent) {
	if candidate.promptID == "" {
		candidate.promptID = event.PromptID
	}
	if candidate.toolName == "" {
		candidate.toolName = event.ToolName
	}
	if candidate.toolUseID == "" {
		candidate.toolUseID = event.ToolUseID
	}
	if candidate.elicitationID == "" {
		candidate.elicitationID = event.ElicitationID
	}
}

func interactionKindsForClose(event NormalizedEvent) []interactionKind {
	switch event.ToolName {
	case "AskUserQuestion":
		return []interactionKind{interactionUser}
	case "":
		return []interactionKind{interactionUser, interactionPermission}
	default:
		return []interactionKind{interactionPermission}
	}
}

func notificationInteractionKind(value string) interactionKind {
	switch normalizedNotification(value) {
	case protocol.InputPermissionRequired:
		return interactionPermission
	case protocol.InputUserRequired:
		return interactionUser
	default:
		return 0
	}
}

func interactionKindName(kind interactionKind) string {
	if kind == interactionUser {
		return "user"
	}
	return "permission"
}

func validAttentionAction(action protocol.TerminalAttentionAction) bool {
	switch action {
	case protocol.TerminalAttentionSubmit, protocol.TerminalAttentionCancel, protocol.TerminalAttentionInterrupt:
		return true
	default:
		return false
	}
}

func normalizedNotification(value string) protocol.InputState {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "permission_prompt", "permission_request", "permission_required":
		return protocol.InputPermissionRequired
	case "elicitation_dialog", "ask_user_question", "user_input_required", "agent_needs_input":
		return protocol.InputUserRequired
	default:
		return protocol.InputNone
	}
}

func clearsAttentionNotification(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "elicitation_complete", "elicitation_response", "agent_completed":
		return true
	default:
		return false
	}
}

func (reducer *Reducer) projectActivity(p *Projection) {
	state := reducer.workActivity
	if state == "" {
		state = protocol.ActivityIdle
	}
	if reducer.settled {
		state = protocol.ActivityCompleted
	}
	switch reducer.inputState {
	case protocol.InputPermissionRequired, protocol.InputUserRequired:
		state = protocol.ActivityNeedsInput
	}
	p.Activity = state
	if state != reducer.activity {
		reducer.activity = state
		p.ActivityChanged = true
	}
}

// Presence is a snapshot of this launch's root process, validated by the monitor.
// It closes stale permission prompts while a foreground tool is still running.
func (reducer *Reducer) ApplyPresence(status string) (p Projection) {
	if reducer.activeSessionID == "" {
		return p
	}
	if reducer.settled && (status == "idle" || status == "shell" || status == "busy") {
		// Residual background presence cannot reopen the turn or dismiss an
		// explicit interaction. Hooks and attention actions resolve those.
		reducer.presence = status
		reducer.setDerivedInput(&p)
		reducer.projectActivity(&p)
		return p
	}
	if (status == "busy" || status == "shell") && reducer.presence == "idle" {
		reducer.failed = false
	}
	reducer.presence = status
	switch status {
	case "busy", "shell":
		reducer.workActivity = protocol.ActivityWorking
		// Retain typed identity so a delayed generic notification cannot reopen
		// a permission already handled, or turn a question into a permission.
		for _, candidate := range reducer.interactions {
			if candidate.agentID == "" {
				candidate.pending = false
			}
		}
		reducer.setDerivedInput(&p)
	case "waiting":
		reducer.workActivity = protocol.ActivityNeedsInput
		if reducer.inputState != protocol.InputPermissionRequired && reducer.inputState != protocol.InputUserRequired {
			state := protocol.InputUserRequired
			if latest := reducer.latestInteraction(func(candidate *interaction) bool { return candidate.typed }); latest != nil && latest.kind == interactionPermission {
				state = protocol.InputPermissionRequired
			}
			reducer.setInput(state, &p)
		}
	case "idle":
		reducer.clearInteractions()
		reducer.workActivity = protocol.ActivityIdle
		input := protocol.InputNone
		if reducer.failed {
			reducer.workActivity = protocol.ActivityFailed
		}
		reducer.setInput(input, &p)
	default:
		reducer.resetSettlement()
		reducer.workActivity = protocol.ActivityUnknown
		reducer.clearInteractions()
		reducer.setInput(protocol.InputNone, &p)
	}
	reducer.projectActivity(&p)
	return p
}

func (reducer *Reducer) resetSettlement() {
	reducer.stopAt = time.Time{}
	reducer.settled, reducer.hookSuccess, reducer.hookContinuation = false, false, false
}

// TranscriptObservation excludes all conversational content. These structural
// fields are internal Claude schemas; unrecognized outcomes never complete work.
type TranscriptObservation struct {
	SessionID             string
	Sidechain             bool
	Timestamp             time.Time
	ContextTokens         *int64
	Interrupted           bool
	StopSummary           bool
	HasOutput             bool
	HasErrors             bool
	AdditionalContext     bool
	PreventedContinuation bool
	StopOutcome           string
}

func (reducer *Reducer) ApplyTranscript(record TranscriptObservation) Projection {
	if record.Sidechain || record.SessionID != reducer.activeSessionID {
		return Projection{}
	}
	projection := Projection{}
	if record.ContextTokens != nil && *record.ContextTokens >= 0 && (reducer.contextTokens == nil || *reducer.contextTokens != *record.ContextTokens) {
		value := *record.ContextTokens
		reducer.contextTokens = &value
		projection.ContextTokens = &value
		projection.ContextChanged = true
	}
	if reducer.stopAt.IsZero() || record.Timestamp.Before(reducer.stopAt) {
		return projection
	}
	if !record.Interrupted && record.StopOutcome == "" && !record.StopSummary {
		return projection
	}
	if record.Interrupted {
		reducer.resetSettlement()
	} else {
		if record.StopOutcome != "" {
			if record.StopOutcome == "hook_success" {
				reducer.hookSuccess = true
			} else {
				reducer.hookContinuation = true
				reducer.settled = false
			}
		}
		if record.StopSummary {
			reducer.settled = !record.HasErrors && !record.AdditionalContext && !record.PreventedContinuation && !reducer.hookContinuation && (!record.HasOutput || reducer.hookSuccess)
		}
	}
	settlement := reducer.ApplyPresence(reducer.presence)
	settlement.ContextTokens = projection.ContextTokens
	settlement.ContextChanged = projection.ContextChanged
	return settlement
}
