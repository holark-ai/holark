package codex

import "github.com/holark-ai/holark/internal/protocol"

// Fact is a transport-independent, generation-scoped observation. Only the
// authenticated terminal bridge can verify a main-thread binding or successor.
type Fact struct {
	Kind, ThreadID, TurnID, RequestID, Status, Predecessor string
	Mode                                                   string
	ContextTokens                                          *int64
	Launch, Connection                                     uint64
	Verified, Baseline, Plan                               bool
}
type Projection struct {
	Activity       protocol.AgentActivity
	Identity       string
	Input          protocol.InputState
	ContextTokens  *int64
	ContextChanged bool
	Changed        bool
}
type Reducer struct {
	Activity           protocol.AgentActivity
	ThreadID           string
	Launch, Connection uint64
	Input              protocol.InputState
	turn               string
	baseline           map[string]bool
	completed          map[string]bool
	plans              map[string]bool
	mode               string
	turnModes          map[string]string
	requests           map[string]pendingRequest
	resolved           map[string]bool
	contextTokens      *int64
}

type pendingRequest struct {
	kind       string
	connection uint64
}

func NewReducer(thread string, launch uint64, input protocol.InputState) *Reducer {
	if input == "" {
		input = protocol.InputNone
	}
	return &Reducer{ThreadID: thread, Launch: launch, Input: input, Activity: protocol.ActivityIdle, baseline: map[string]bool{}, completed: map[string]bool{}, plans: map[string]bool{}, turnModes: map[string]string{}, requests: map[string]pendingRequest{}, resolved: map[string]bool{}}
}
func (r *Reducer) Apply(f Fact) Projection {
	p := Projection{}
	if f.Launch != r.Launch || f.Connection < r.Connection {
		return p
	}
	r.Connection = f.Connection
	if f.Kind == "binding" {
		if !f.Verified || f.ThreadID == "" || (r.ThreadID != "" && r.ThreadID != f.ThreadID && f.Predecessor != r.ThreadID) {
			return p
		}
		if r.ThreadID != f.ThreadID {
			r.ThreadID = f.ThreadID
			r.contextTokens = nil
			r.turn = ""
			r.activity(protocol.ActivityIdle, &p)
			clear(r.requests)
			clear(r.resolved)
			r.set(protocol.InputNone, &p)
		}
		p.Identity = f.ThreadID
		return p
	}
	if f.ThreadID != r.ThreadID || f.ThreadID == "" {
		return p
	}
	key := f.ThreadID + "/" + f.TurnID
	if f.Baseline {
		if f.TurnID != "" {
			r.baseline[key] = true
		}
		return p
	}
	switch f.Kind {
	case "context_usage":
		if f.ContextTokens == nil || *f.ContextTokens < 0 || (r.contextTokens != nil && *r.contextTokens == *f.ContextTokens) {
			return p
		}
		value := *f.ContextTokens
		r.contextTokens = &value
		p.ContextTokens = &value
		p.ContextChanged = true
	case "settings":
		if f.Mode != "" {
			r.mode = f.Mode
			if r.turn != "" && !r.completed[r.ThreadID+"/"+r.turn] {
				r.turnModes[r.ThreadID+"/"+r.turn] = f.Mode
			}
		}
	case "turn_started":
		if r.baseline[key] || r.completed[key] || r.turn == f.TurnID {
			return p
		}
		r.turn = f.TurnID
		if _, exists := r.turnModes[key]; !exists {
			r.turnModes[key] = r.mode
		}
		clear(r.requests)
		r.set(protocol.InputNone, &p)
		r.activity(protocol.ActivityWorking, &p)
	case "plan_completed":
		if !r.baseline[key] && f.Plan {
			r.plans[key] = true
		}
	case "turn_completed":
		if r.baseline[key] || r.completed[key] || (r.turn != "" && r.turn != f.TurnID) {
			return p
		}
		r.completed[key] = true
		clear(r.requests)
		r.turn = f.TurnID
		switch f.Status {
		case "completed":
			if r.plans[key] && r.turnModes[key] == "plan" {
				r.set(protocol.InputUserRequired, &p)
			} else {
				r.set(protocol.InputTaskComplete, &p)
			}
		default:
			r.set(protocol.InputNone, &p)
			r.activity(protocol.ActivityIdle, &p)
			if f.Status == "failed" {
				r.activity(protocol.ActivityFailed, &p)
			}
		}
	case "question", "permission":
		if r.resolved[f.RequestID] || (f.TurnID != "" && (r.completed[key] || r.baseline[key])) {
			return p
		}
		r.requests[f.RequestID] = pendingRequest{kind: f.Kind, connection: f.Connection}
		r.attention(&p)
	case "requests_recovered":
		removed := false
		for id, request := range r.requests {
			if request.connection < f.Connection {
				delete(r.requests, id)
				removed = true
			}
		}
		// Only stale requests invalidate attention. A completed turn or proposed
		// plan with no pending requests retains its reconciled input state.
		if removed {
			r.attention(&p)
		}
	case "resolved":
		r.resolved[f.RequestID] = true
		if _, ok := r.requests[f.RequestID]; ok {
			delete(r.requests, f.RequestID)
			r.attention(&p)
		}
	}
	return p
}
func (r *Reducer) attention(p *Projection) {
	state := protocol.InputNone
	for _, request := range r.requests {
		if request.kind == "question" {
			state = protocol.InputUserRequired
			break
		}
		state = protocol.InputPermissionRequired
	}
	r.set(state, p)
	if state == protocol.InputNone && r.turn != "" && !r.completed[r.ThreadID+"/"+r.turn] {
		r.activity(protocol.ActivityWorking, p)
	} else if state == protocol.InputNone {
		r.activity(protocol.ActivityIdle, p)
	}
}
func (r *Reducer) activity(state protocol.AgentActivity, p *Projection) {
	if r.Activity != state {
		r.Activity = state
		p.Activity = state
		p.Changed = true
	}
}
func (r *Reducer) set(state protocol.InputState, p *Projection) {
	switch state {
	case protocol.InputPermissionRequired, protocol.InputUserRequired:
		r.activity(protocol.ActivityNeedsInput, p)
	case protocol.InputTaskComplete:
		r.activity(protocol.ActivityCompleted, p)
	}

	if state != r.Input {
		r.Input = state
		p.Input = state
		p.Changed = true
	}
}
