package opencode

import (
	"testing"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestReducerResumesAfterCommitGuidance(t *testing.T) {
	for _, status := range []string{"busy", "retry"} {
		t.Run(status, func(t *testing.T) {
			reducer := NewReducer("root", protocol.InputUserRequired)
			waiting := reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: "idle"})
			if waiting.Activity != protocol.ActivityNeedsInput || waiting.InputChanged {
				t.Fatalf("restored guidance projection = %#v", waiting)
			}
			working := reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: status})
			if !working.ActivityChanged || working.Activity != protocol.ActivityWorking || !working.InputChanged || working.InputState != protocol.InputNone {
				t.Fatalf("resumed projection = %#v", working)
			}
			completed := reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: "idle"})
			if !completed.ActivityChanged || completed.Activity != protocol.ActivityCompleted || !completed.InputChanged || completed.InputState != protocol.InputTaskComplete {
				t.Fatalf("completed projection = %#v", completed)
			}
		})
	}
}

func TestReducerKeepsPendingInteractionsWhenResuming(t *testing.T) {
	for _, kind := range []string{"question", "permission"} {
		t.Run(kind, func(t *testing.T) {
			reducer := NewReducer("root", protocol.InputUserRequired)
			reducer.Apply(Fact{Type: kind + "_opened", SessionID: "root", RequestID: "request"})
			for _, status := range []string{"busy", "retry"} {
				working := reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: status})
				if working.ActivityChanged || working.InputChanged {
					t.Fatalf("%s cleared pending %s: %#v", status, kind, working)
				}
			}
			answered := reducer.Apply(Fact{Type: kind + "_closed", SessionID: "root", RequestID: "request"})
			if answered.Activity != protocol.ActivityWorking || !answered.InputChanged || answered.InputState != protocol.InputNone {
				t.Fatalf("answered projection = %#v", answered)
			}
		})
	}
}

func TestReducerRecoversFromRootErrorWithinTurn(t *testing.T) {
	reducer := NewReducer("root", protocol.InputNone)
	reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: "busy"})
	reducer.Apply(Fact{Type: "session_error", SessionID: "root", Outcome: "error"})
	working := reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: "busy"})
	if working.Activity != protocol.ActivityWorking {
		t.Fatalf("recovery activity = %q, want working", working.Activity)
	}
	completed := reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: "idle"})
	if completed.Activity != protocol.ActivityCompleted || completed.InputState != protocol.InputTaskComplete {
		t.Fatalf("completed projection = %#v", completed)
	}
}

func TestReducerKeepsUnrecoveredRootErrorsTerminal(t *testing.T) {
	tests := []struct {
		name     string
		outcome  string
		activity protocol.AgentActivity
	}{
		{name: "error", outcome: "error", activity: protocol.ActivityFailed},
		{name: "interruption", outcome: "interrupted", activity: protocol.ActivityIdle},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reducer := NewReducer("root", protocol.InputNone)
			reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: "busy"})
			reducer.Apply(Fact{Type: "session_error", SessionID: "root", Outcome: test.outcome})
			reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: "idle"})
			if reducer.activity != test.activity || reducer.inputState != protocol.InputNone {
				t.Fatalf("terminal activity = %q, input = %q", reducer.activity, reducer.inputState)
			}
		})
	}
}

func TestReducerDoesNotClearBackgroundFailureWhenRootResumesWork(t *testing.T) {
	reducer := NewReducer("root", protocol.InputNone)
	reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: "busy"})
	reducer.Apply(Fact{Type: "background_task_started", SessionID: "child", ParentID: "root"})
	reducer.Apply(Fact{Type: "background_task_settled", SessionID: "child", Outcome: "error"})
	reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: "busy"})
	terminal := reducer.Apply(Fact{Type: "session_status", SessionID: "root", Status: "idle"})
	if terminal.Activity != protocol.ActivityFailed || reducer.inputState != protocol.InputNone {
		t.Fatalf("background failure projection = %#v, input = %q", terminal, reducer.inputState)
	}
}

// Event delivery and route hydration are independent; deterministic interleaving
// here covers races that real terminal interaction cannot reliably schedule.
func TestTerminalSelectionRetainsEventsBeforeRouteAndIsolatesState(t *testing.T) {
	r := NewReducer("", protocol.InputNone)
	r.Apply(Fact{Type: "observer_initialized", Status: "terminal"})
	tokens := int64(42)
	for _, f := range []Fact{
		{Type: "session_created", SessionID: "a"},
		{Type: "session_status", SessionID: "a", Status: "busy"},
		{Type: "permission_opened", SessionID: "a", RequestID: "request"},
		{Type: "context_usage", SessionID: "a", MessageID: "message", ContextTokens: &tokens},
	} {
		if p := r.Apply(f); p != (Projection{}) {
			t.Fatalf("unselected session published %+v", p)
		}
	}
	a := r.Apply(Fact{Type: "session_selected", SessionID: "a"})
	if !a.Metadata || a.ResumeTarget != "a" || a.Activity != protocol.ActivityNeedsInput || a.InputState != protocol.InputPermissionRequired || a.ContextTokens == nil || *a.ContextTokens != 42 {
		t.Fatalf("selected state: %+v", a)
	}
	r.Apply(Fact{Type: "session_created", SessionID: "b"})
	b := r.Apply(Fact{Type: "session_selected", SessionID: "b"})
	if !b.Metadata || b.Activity != protocol.ActivityIdle || b.InputState != protocol.InputNone || !b.ContextChanged || b.ContextTokens != nil {
		t.Fatalf("stale state crossed selection: %+v", b)
	}
	for _, f := range []Fact{{Type: "permission_closed", SessionID: "a", RequestID: "request"}, {Type: "session_status", SessionID: "a", Status: "idle"}} {
		if p := r.Apply(f); p != (Projection{}) {
			t.Fatalf("background conversation published %+v", p)
		}
	}
	a = r.Apply(Fact{Type: "session_selected", SessionID: "a"})
	if a.Activity != protocol.ActivityCompleted || a.InputState != protocol.InputTaskComplete {
		t.Fatalf("lost completion: %+v", a)
	}
}

func TestTerminalSelectionAggregatesChildrenWithoutFollowingThem(t *testing.T) {
	r := NewReducer("", protocol.InputNone)
	r.Apply(Fact{Type: "observer_initialized", Status: "terminal"})
	r.Apply(Fact{Type: "session_selected", SessionID: "a"})
	r.Apply(Fact{Type: "background_task_started", SessionID: "child", ParentID: "a"})
	r.Apply(Fact{Type: "session_status", SessionID: "a", Status: "idle"})
	r.Apply(Fact{Type: "session_selected", SessionID: "b"})
	if p := r.Apply(Fact{Type: "session_selected", SessionID: "child"}); p != (Projection{}) {
		t.Fatalf("followed subagent: %+v", p)
	}
	if p := r.Apply(Fact{Type: "permission_opened", SessionID: "child", RequestID: "permission"}); p != (Projection{}) {
		t.Fatalf("child contaminated selected root: %+v", p)
	}
	a := r.Apply(Fact{Type: "session_selected", SessionID: "a"})
	if a.InputState != protocol.InputPermissionRequired {
		t.Fatalf("lost child attention: %+v", a)
	}
	r.Apply(Fact{Type: "permission_closed", SessionID: "child", RequestID: "permission"})
	done := r.Apply(Fact{Type: "background_task_settled", SessionID: "child", Outcome: "completed"})
	if done.Activity != protocol.ActivityCompleted {
		t.Fatalf("lost background completion: %+v", done)
	}
}

func TestTerminalSelectionDoesNotCompleteImportedHistory(t *testing.T) {
	r := NewReducer("saved", protocol.InputPermissionRequired)
	r.Apply(Fact{Type: "observer_initialized", Status: "terminal"})
	r.Apply(Fact{Type: "session_status", SessionID: "saved", Status: "idle"})
	p := r.Apply(Fact{Type: "session_selected", SessionID: "saved"})
	if p.Activity != protocol.ActivityIdle || p.InputState != protocol.InputNone {
		t.Fatalf("invented imported completion or stale attention: %+v", p)
	}
}
