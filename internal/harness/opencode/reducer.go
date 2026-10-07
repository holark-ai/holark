package opencode

import (
	"github.com/holark-ai/holark/internal/protocol"
)

type Projection struct {
	Activity        protocol.AgentActivity
	ActivityChanged bool
	Metadata        bool
	ResumeTarget    string
	InputChanged    bool
	InputState      protocol.InputState
	ContextChanged  bool
	ContextTokens   *int64
}

type interactionKind uint8

const (
	interactionQuestion interactionKind = iota + 1
	interactionPermission
)

type pendingInteraction struct {
	kind     interactionKind
	sequence uint64
}

// Reducer retains each conversation independently while the terminal moves
// between routes. The minimal UI continues to bind its first created root.
type Reducer struct {
	*sessionReducer
	terminal bool
	sessions map[string]*sessionReducer
}

func NewReducer(resumeTarget string, inputState protocol.InputState) *Reducer {
	state := newSessionReducer(resumeTarget, inputState)
	return &Reducer{sessionReducer: state, sessions: map[string]*sessionReducer{resumeTarget: state}}
}

func (r *Reducer) Apply(fact Fact) Projection {
	if r == nil {
		return Projection{}
	}
	if fact.Type == "observer_initialized" {
		r.terminal = fact.Status == "terminal"
		if r.terminal {
			saved := r.sessionReducer
			r.sessionReducer = newSessionReducer("", protocol.InputNone)
			r.sessions = map[string]*sessionReducer{"": r.sessionReducer}
			// Hydration rebuilds live requests, but idle history cannot restore
			// completion. Keep it only for the matching resumed conversation.
			if saved.rootID != "" && saved.inputState == protocol.InputTaskComplete {
				r.session(saved.rootID).inputState = protocol.InputTaskComplete
			}
		}
		return Projection{}
	}
	if !r.terminal {
		return r.sessionReducer.Apply(fact)
	}
	if fact.Type == "session_selected" {
		state := r.session(fact.SessionID)
		if state.parents[fact.SessionID] != "" {
			return Projection{}
		}
		if state == r.sessionReducer {
			return Projection{}
		}
		r.sessionReducer = state
		p := Projection{Metadata: true, ResumeTarget: state.rootID,
			InputChanged: true, InputState: state.inputState,
			ContextChanged: true, ContextTokens: state.contextTokens}
		state.activity = ""
		state.projectActivity(&p)
		return p
	}
	if fact.SessionID == "" {
		return Projection{}
	}
	r.session(fact.SessionID)
	if fact.ParentID != "" {
		r.session(fact.ParentID)
	}
	var selected Projection
	for _, state := range r.sessions {
		// The unbound placeholder must never choose a session from creation alone.
		if state.rootID == "" {
			continue
		}
		p := state.Apply(fact)
		if state == r.sessionReducer {
			selected = p
		}
	}
	return selected
}

func (r *Reducer) session(id string) *sessionReducer {
	if state := r.sessions[id]; state != nil {
		return state
	}
	state := newSessionReducer(id, protocol.InputNone)
	state.parents = r.parents
	r.sessions[id] = state
	return state
}

type sessionReducer struct {
	activity         protocol.AgentActivity
	workActivity     protocol.AgentActivity
	rootID           string
	turnStarted      bool
	rootIdle         bool
	turnFailed       bool
	turnInterrupted  bool
	backgroundFailed bool
	parents          map[string]string
	activeBackground map[string]bool
	questions        map[string]pendingInteraction
	permissions      map[string]pendingInteraction
	sequence         uint64
	inputState       protocol.InputState
	contextTokens    *int64
	usageMessages    map[string]bool
}

func newSessionReducer(resumeTarget string, inputState protocol.InputState) *sessionReducer {
	switch inputState {
	case protocol.InputNone, protocol.InputPermissionRequired, protocol.InputUserRequired, protocol.InputTaskComplete:
	default:
		inputState = protocol.InputNone
	}
	return &sessionReducer{
		rootID:           resumeTarget,
		parents:          make(map[string]string),
		activeBackground: make(map[string]bool),
		questions:        make(map[string]pendingInteraction),
		permissions:      make(map[string]pendingInteraction),
		usageMessages:    make(map[string]bool),
		inputState:       inputState,
	}
}

func (reducer *sessionReducer) Apply(fact Fact) (projection Projection) {
	if reducer == nil {
		return Projection{}
	}
	defer func() { reducer.projectActivity(&projection) }()
	switch fact.Type {
	case "context_usage":
		if fact.SessionID != reducer.rootID || fact.MessageID == "" || fact.ContextTokens == nil || *fact.ContextTokens < 0 || reducer.usageMessages[fact.MessageID] {
			return projection
		}
		reducer.usageMessages[fact.MessageID] = true
		if reducer.contextTokens != nil && *reducer.contextTokens == *fact.ContextTokens {
			return projection
		}
		value := *fact.ContextTokens
		reducer.contextTokens = &value
		projection.ContextTokens = &value
		projection.ContextChanged = true
	case "session_created":
		if fact.ParentID != "" {
			reducer.recordParent(fact.SessionID, fact.ParentID)
		}
		if reducer.rootID == "" && fact.ParentID == "" {
			reducer.rootID = fact.SessionID
			projection.Metadata = true
			projection.ResumeTarget = fact.SessionID
		}
	case "background_task_started":
		reducer.recordParent(fact.SessionID, fact.ParentID)
		reducer.activeBackground[fact.SessionID] = true
		if reducer.managed(fact.SessionID) {
			reducer.workActivity = protocol.ActivityWorking
			reducer.turnStarted = true
		}
	case "background_task_settled":
		delete(reducer.activeBackground, fact.SessionID)
		if reducer.managed(fact.SessionID) && fact.Outcome == "error" {
			reducer.backgroundFailed = true
		}
		if reducer.rootIdle {
			reducer.applyStatus(Fact{SessionID: reducer.rootID, Status: "idle"}, &projection)
		}
	case "session_error":
		if fact.SessionID == reducer.rootID {
			reducer.turnFailed = true
			reducer.turnInterrupted = fact.Outcome == "interrupted"
			reducer.workActivity = protocol.ActivityFailed
			if reducer.turnInterrupted {
				reducer.workActivity = protocol.ActivityIdle
			}
		}
	case "session_status":
		reducer.applyStatus(fact, &projection)
	case "question_opened":
		reducer.open(fact, interactionQuestion, &projection)
	case "question_closed":
		reducer.close(fact, interactionQuestion, &projection)
	case "permission_opened":
		reducer.open(fact, interactionPermission, &projection)
	case "permission_closed":
		reducer.close(fact, interactionPermission, &projection)
	}
	return projection
}

func (reducer *sessionReducer) applyStatus(fact Fact, projection *Projection) {
	if reducer.rootID == "" || fact.SessionID != reducer.rootID {
		return
	}
	switch fact.Status {
	case "busy", "retry":
		if !reducer.turnStarted {
			reducer.backgroundFailed = false
		}
		// A later root working status means OpenCode recovered from a root
		// session error without ending the turn. Background failures remain
		// terminal for that turn and are tracked separately.
		reducer.turnFailed = false
		reducer.turnInterrupted = false
		reducer.turnStarted = true
		reducer.rootIdle = false
		reducer.workActivity = protocol.ActivityWorking
		// Restored commit guidance has no pending question. Clear it when
		// work resumes, while retaining attention for open interactions.
		if reducer.inputState == protocol.InputTaskComplete || reducer.inputState == protocol.InputUserRequired {
			reducer.setInput(reducer.derivedAttention(), projection)
		}
	case "idle":
		reducer.rootIdle = true
		if !reducer.turnStarted || reducer.hasActiveBackground() {
			return
		}
		clear(reducer.questions)
		clear(reducer.permissions)
		reducer.turnStarted = false
		if reducer.turnFailed || reducer.backgroundFailed {
			reducer.workActivity = protocol.ActivityFailed
			if reducer.turnInterrupted && !reducer.backgroundFailed {
				reducer.workActivity = protocol.ActivityIdle
			}
			reducer.setInput(protocol.InputNone, projection)
		} else {
			reducer.workActivity = protocol.ActivityCompleted
			reducer.setInput(protocol.InputTaskComplete, projection)
		}
	}
}

func (reducer *sessionReducer) open(fact Fact, kind interactionKind, projection *Projection) {
	if !reducer.managed(fact.SessionID) {
		return
	}
	target := reducer.questions
	if kind == interactionPermission {
		target = reducer.permissions
	}
	if _, exists := target[fact.RequestID]; exists {
		return
	}
	reducer.sequence++
	target[fact.RequestID] = pendingInteraction{kind: kind, sequence: reducer.sequence}
	reducer.setInput(reducer.derivedAttention(), projection)
}

func (reducer *sessionReducer) close(fact Fact, kind interactionKind, projection *Projection) {
	if !reducer.managed(fact.SessionID) {
		return
	}
	target := reducer.questions
	if kind == interactionPermission {
		target = reducer.permissions
	}
	if _, exists := target[fact.RequestID]; !exists {
		return
	}
	delete(target, fact.RequestID)
	reducer.setInput(reducer.derivedAttention(), projection)
}

func (reducer *sessionReducer) recordParent(sessionID, parentID string) {
	if sessionID == "" || parentID == "" || sessionID == parentID {
		return
	}
	previous, hadPrevious := reducer.parents[sessionID]
	reducer.parents[sessionID] = parentID
	if reducer.parentChainCycles(sessionID) {
		if hadPrevious {
			reducer.parents[sessionID] = previous
		} else {
			delete(reducer.parents, sessionID)
		}
	}
}

func (reducer *sessionReducer) parentChainCycles(sessionID string) bool {
	visited := map[string]bool{}
	for current := sessionID; current != ""; current = reducer.parents[current] {
		if visited[current] {
			return true
		}
		visited[current] = true
	}
	return false
}

func (reducer *sessionReducer) managed(sessionID string) bool {
	if reducer.rootID == "" || sessionID == "" {
		return false
	}
	visited := map[string]bool{}
	for current := sessionID; current != ""; current = reducer.parents[current] {
		if current == reducer.rootID {
			return true
		}
		if visited[current] {
			return false
		}
		visited[current] = true
	}
	return false
}

func (reducer *sessionReducer) hasActiveBackground() bool {
	for sessionID := range reducer.activeBackground {
		if reducer.managed(sessionID) {
			return true
		}
	}
	return false
}

func (reducer *sessionReducer) derivedAttention() protocol.InputState {
	var latest pendingInteraction
	found := false
	for _, pending := range reducer.questions {
		if !found || pending.sequence > latest.sequence {
			latest = pending
			found = true
		}
	}
	for _, pending := range reducer.permissions {
		if !found || pending.sequence > latest.sequence {
			latest = pending
			found = true
		}
	}
	if !found {
		return protocol.InputNone
	}
	if latest.kind == interactionQuestion {
		return protocol.InputUserRequired
	}
	return protocol.InputPermissionRequired
}

func (reducer *sessionReducer) setInput(state protocol.InputState, projection *Projection) {
	if state == reducer.inputState {
		return
	}
	reducer.inputState = state
	projection.InputChanged = true
	projection.InputState = state
}

func (reducer *sessionReducer) projectActivity(p *Projection) {
	state := reducer.workActivity
	if state == "" {
		state = protocol.ActivityIdle
	}
	switch reducer.inputState {
	case protocol.InputPermissionRequired, protocol.InputUserRequired:
		state = protocol.ActivityNeedsInput
	}
	if state != reducer.activity {
		reducer.activity = state
		p.Activity = state
		p.ActivityChanged = true
	}
}
