package claudecode

import (
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestReducerSettlesWhileStopReportsBackgroundWork(t *testing.T) {
	tests := []struct {
		name string
		stop NormalizedEvent
	}{
		{name: "unknown background tasks", stop: NormalizedEvent{}},
		{name: "running background task", stop: NormalizedEvent{BackgroundTasksKnown: true, BackgroundTaskCount: 1}},
		{name: "running session cron", stop: NormalizedEvent{BackgroundTasksKnown: true, SessionCronsKnown: true, SessionCronCount: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reducer := startedReducer(t)
			reducer.ApplyPresence("idle")
			test.stop.Event = "Stop"
			test.stop.SessionID = reducerSessionID
			test.stop.SentAt = 1_000
			reducer.Apply(test.stop)

			projection := reducer.ApplyTranscript(TranscriptObservation{
				SessionID:   reducerSessionID,
				Timestamp:   time.UnixMilli(1_000),
				StopSummary: true,
			})
			if projection.InputState != protocol.InputTaskComplete || projection.Activity != protocol.ActivityCompleted {
				t.Fatalf("finished turn was not reported complete with background work: %#v", projection)
			}
		})
	}
}

func TestTranscriptUsageProjectsOnlyCurrentMainSessionInputContext(t *testing.T) {
	record, err := decodeTranscript([]byte(`{
		"type":"assistant",
		"sessionId":"123e4567-e89b-12d3-a456-426614174000",
		"isSidechain":false,
		"timestamp":"2026-09-15T12:00:00Z",
		"message":{"role":"assistant","content":[{"type":"text","text":"not retained"}],"usage":{
			"input_tokens":2,
			"cache_creation_input_tokens":1577,
			"cache_read_input_tokens":4970,
			"output_tokens":1933
		}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	reducer := startedReducer(t)
	projection := reducer.ApplyTranscript(record)
	if !projection.ContextChanged || projection.ContextTokens == nil || *projection.ContextTokens != 6549 {
		t.Fatalf("context projection = %#v", projection)
	}
	if duplicate := reducer.ApplyTranscript(record); duplicate.ContextChanged {
		t.Fatalf("duplicate usage changed context: %#v", duplicate)
	}
	otherValue := int64(7000)
	for _, ignored := range []TranscriptObservation{
		{SessionID: reducerSessionID, Sidechain: true, ContextTokens: &otherValue},
		{SessionID: "other-session", ContextTokens: &otherValue},
	} {
		if projection := reducer.ApplyTranscript(ignored); projection.ContextChanged {
			t.Fatalf("foreign usage changed context: %#v", projection)
		}
	}
}

const reducerSessionID = "123e4567-e89b-12d3-a456-426614174000"

func TestReducerTracksSessionUUIDTransitionsAndTranscriptDiagnostics(t *testing.T) {
	reducer := NewReducer(protocol.InputNone)
	started := reducer.Apply(NormalizedEvent{Event: "SessionStart", SessionID: reducerSessionID, Source: "startup", TranscriptPath: "/tmp/start.jsonl"})
	if !started.Metadata || started.ResumeTarget != reducerSessionID || started.RolloutPath != "/tmp/start.jsonl" {
		t.Fatalf("startup projection = %#v", started)
	}
	for _, transition := range []struct {
		source string
		id     string
	}{
		{source: "clear", id: "223e4567-e89b-12d3-a456-426614174000"},
		{source: "resume", id: "323e4567-e89b-12d3-a456-426614174000"},
		{source: "compact", id: "423e4567-e89b-12d3-a456-426614174000"},
		{source: "fork", id: "523e4567-e89b-12d3-a456-426614174000"},
	} {
		projection := reducer.Apply(NormalizedEvent{Event: "SessionStart", SessionID: transition.id, Source: transition.source, TranscriptPath: "/tmp/" + transition.source + ".jsonl"})
		if !projection.Metadata || projection.ResumeTarget != transition.id {
			t.Fatalf("%s projection = %#v", transition.source, projection)
		}
	}
	unknown := reducer.Apply(NormalizedEvent{Event: "FutureHook", SessionID: "523e4567-e89b-12d3-a456-426614174000"})
	if unknown.Metadata || unknown.InputChanged {
		t.Fatalf("unknown hook changed state: %#v", unknown)
	}
}

func TestReducerPreservesInheritedCompletionOnSessionStart(t *testing.T) {
	reducer := NewReducer(protocol.InputTaskComplete)
	projection := reducer.Apply(NormalizedEvent{
		Event: "SessionStart", SessionID: reducerSessionID, Source: "resume",
	})
	if !projection.Metadata {
		t.Fatalf("startup projection = %#v", projection)
	}
	if projection.InputChanged || reducer.inputState != protocol.InputTaskComplete {
		t.Fatalf("startup changed completed input state: projection=%#v state=%q", projection, reducer.inputState)
	}
}

func TestReducerClearsInheritedAttentionOnSessionStart(t *testing.T) {
	for _, state := range []protocol.InputState{protocol.InputPermissionRequired, protocol.InputUserRequired} {
		t.Run(string(state), func(t *testing.T) {
			reducer := NewReducer(state)
			projection := reducer.Apply(NormalizedEvent{
				Event: "SessionStart", SessionID: reducerSessionID, Source: "resume",
			})
			if !projection.Metadata {
				t.Fatalf("startup projection = %#v", projection)
			}
			assertInputProjection(t, projection, protocol.InputNone)
		})
	}
}

func TestReducerProjectsPermissionQuestionAndElicitationAttention(t *testing.T) {
	reducer := startedReducer(t)
	permission := reducer.Apply(NormalizedEvent{Event: "PermissionRequest", SessionID: reducerSessionID, ToolName: "Bash"})
	assertInputProjection(t, permission, protocol.InputPermissionRequired)
	accepted := reducer.Apply(NormalizedEvent{Event: "PostToolUse", SessionID: reducerSessionID, ToolName: "Bash", ToolUseID: "tool-one"})
	assertInputProjection(t, accepted, protocol.InputNone)

	permission = reducer.Apply(NormalizedEvent{Event: "PermissionRequest", SessionID: reducerSessionID, ToolName: "Write"})
	assertInputProjection(t, permission, protocol.InputPermissionRequired)
	denied := reducer.Apply(NormalizedEvent{Event: "PermissionDenied", SessionID: reducerSessionID})
	assertInputProjection(t, denied, protocol.InputNone)

	question := reducer.Apply(NormalizedEvent{Event: "PreToolUse", SessionID: reducerSessionID, ToolName: "AskUserQuestion", ToolUseID: "question-one"})
	assertInputProjection(t, question, protocol.InputUserRequired)
	questionPermission := reducer.Apply(NormalizedEvent{Event: "PermissionRequest", SessionID: reducerSessionID, ToolName: "AskUserQuestion"})
	if questionPermission.InputChanged {
		t.Fatalf("AskUserQuestion permission changed input state: %#v", questionPermission)
	}
	answered := reducer.Apply(NormalizedEvent{Event: "PostToolUse", SessionID: reducerSessionID, ToolName: "AskUserQuestion", ToolUseID: "question-one"})
	assertInputProjection(t, answered, protocol.InputNone)

	elicitation := reducer.Apply(NormalizedEvent{Event: "Elicitation", SessionID: reducerSessionID, ElicitationID: "elicit-one"})
	assertInputProjection(t, elicitation, protocol.InputUserRequired)
	result := reducer.Apply(NormalizedEvent{Event: "ElicitationResult", SessionID: reducerSessionID, ElicitationID: "elicit-one", Action: "accept"})
	assertInputProjection(t, result, protocol.InputNone)
}

func TestReducerClassifiesAskUserQuestionPermissionAsUserInput(t *testing.T) {
	reducer := startedReducer(t)
	question := reducer.Apply(NormalizedEvent{Event: "PermissionRequest", SessionID: reducerSessionID, ToolName: "AskUserQuestion"})
	assertInputProjection(t, question, protocol.InputUserRequired)
	answered := reducer.Apply(NormalizedEvent{Event: "PostToolUse", SessionID: reducerSessionID, ToolName: "AskUserQuestion", ToolUseID: "question-one"})
	assertInputProjection(t, answered, protocol.InputNone)
}

func TestReducerIncludesSubagentAttentionButNeverSubagentCompletion(t *testing.T) {
	reducer := startedReducer(t)
	prompt := reducer.Apply(NormalizedEvent{Event: "PermissionRequest", SessionID: reducerSessionID, AgentID: "agent-one"})
	assertInputProjection(t, prompt, protocol.InputPermissionRequired)
	stopped := reducer.Apply(NormalizedEvent{Event: "SubagentStop", SessionID: reducerSessionID, AgentID: "agent-one", BackgroundTasksKnown: true})
	assertInputProjection(t, stopped, protocol.InputNone)
	resolved := reducer.Apply(NormalizedEvent{Event: "PermissionDenied", SessionID: reducerSessionID, AgentID: "agent-one", ToolUseID: "tool-one"})
	if resolved.InputChanged {
		t.Fatalf("late subagent denial changed input state: %#v", resolved)
	}
	mainNamedAsSubagent := reducer.Apply(NormalizedEvent{Event: "Stop", SessionID: reducerSessionID, AgentID: "agent-one", BackgroundTasksKnown: true})
	if mainNamedAsSubagent.InputChanged {
		t.Fatalf("agent-scoped Stop completed session: %#v", mainNamedAsSubagent)
	}
}

func TestReducerPreservesSubagentAttentionWhenRootBecomesBusy(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		open       NormalizedEvent
		resolve    NormalizedEvent
		inputState protocol.InputState
	}{
		{
			name: "permission while root is busy", status: "busy", inputState: protocol.InputPermissionRequired,
			open:    NormalizedEvent{Event: "PermissionRequest", AgentID: "agent-one", ToolName: "Bash", ToolUseID: "tool-one"},
			resolve: NormalizedEvent{Event: "PermissionDenied", AgentID: "agent-one", ToolName: "Bash", ToolUseID: "tool-one"},
		},
		{
			name: "question while root runs a shell", status: "shell", inputState: protocol.InputUserRequired,
			open:    NormalizedEvent{Event: "PreToolUse", AgentID: "agent-one", ToolName: "AskUserQuestion", ToolUseID: "question-one"},
			resolve: NormalizedEvent{Event: "SubagentStop", AgentID: "agent-one", BackgroundTasksKnown: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reducer := startedReducer(t)
			test.open.SessionID = reducerSessionID
			assertInputProjection(t, reducer.Apply(test.open), test.inputState)

			projection := reducer.ApplyPresence(test.status)
			if reducer.inputState != test.inputState || projection.Activity != protocol.ActivityNeedsInput {
				t.Fatalf("root presence cleared subagent attention: state=%q projection=%#v", reducer.inputState, projection)
			}

			test.resolve.SessionID = reducerSessionID
			assertInputProjection(t, reducer.Apply(test.resolve), protocol.InputNone)
		})
	}
}

func TestReducerProjectsSubagentNotificationAttention(t *testing.T) {
	reducer := startedReducer(t)
	attention := reducer.Apply(NormalizedEvent{Event: "Notification", SessionID: reducerSessionID, NotificationType: "agent_needs_input"})
	assertInputProjection(t, attention, protocol.InputUserRequired)
	completed := reducer.Apply(NormalizedEvent{Event: "Notification", SessionID: reducerSessionID, NotificationType: "agent_completed"})
	assertInputProjection(t, completed, protocol.InputNone)
}

func TestReducerNewPromptClearsPendingAttention(t *testing.T) {
	reducer := startedReducer(t)
	reducer.Apply(NormalizedEvent{Event: "PermissionRequest", SessionID: reducerSessionID})
	projection := reducer.Apply(NormalizedEvent{Event: "UserPromptSubmit", SessionID: reducerSessionID})
	assertInputProjection(t, projection, protocol.InputNone)
	foreign := reducer.Apply(NormalizedEvent{Event: "PermissionRequest", SessionID: "other-session", ToolUseID: "tool-two"})
	if foreign.InputChanged {
		t.Fatalf("foreign session changed reducer: %#v", foreign)
	}
}

func TestReducerKeepsTypedQuestionAheadOfDelayedPermissionNotification(t *testing.T) {
	reducer := startedReducer(t)
	question := NormalizedEvent{
		Event: "PreToolUse", SessionID: reducerSessionID, PromptID: "prompt-one",
		ToolName: "AskUserQuestion", ToolUseID: "question-one",
	}
	assertInputProjection(t, reducer.Apply(question), protocol.InputUserRequired)

	pairedPermission := reducer.Apply(NormalizedEvent{
		Event: "PermissionRequest", SessionID: reducerSessionID, PromptID: "prompt-one",
		ToolName: "AskUserQuestion",
	})
	if pairedPermission.InputChanged {
		t.Fatalf("paired AskUserQuestion permission changed state: %#v", pairedPermission)
	}
	delayedNotification := NormalizedEvent{
		Event: "Notification", SessionID: reducerSessionID, PromptID: "prompt-one",
		NotificationType: "permission_prompt",
	}
	if projection := reducer.Apply(delayedNotification); projection.InputChanged {
		t.Fatalf("delayed generic notification overrode typed question: %#v", projection)
	}

	assertInputProjection(t, reducer.ApplyAttentionAction(protocol.TerminalAttentionCancel), protocol.InputNone)
	if projection := reducer.Apply(delayedNotification); projection.InputChanged {
		t.Fatalf("delayed notification reopened handled question: %#v", projection)
	}
	if p := reducer.Apply(NormalizedEvent{Event: "Stop", SessionID: reducerSessionID, BackgroundTasksKnown: true}); p.InputChanged {
		t.Fatalf("Stop alone completed: %+v", p)
	}
}

func TestReducerAttentionActionsClearVisibleInteractionByPriority(t *testing.T) {
	reducer := startedReducer(t)
	assertInputProjection(t, reducer.Apply(NormalizedEvent{
		Event: "PermissionRequest", SessionID: reducerSessionID, ToolName: "Bash",
	}), protocol.InputPermissionRequired)
	assertInputProjection(t, reducer.Apply(NormalizedEvent{
		Event: "PreToolUse", SessionID: reducerSessionID, ToolName: "AskUserQuestion", ToolUseID: "question-one",
	}), protocol.InputUserRequired)

	// User questions have the same priority as the Codex harness. Handling the
	// visible question reveals any independent permission still underneath it.
	assertInputProjection(t, reducer.ApplyAttentionAction(protocol.TerminalAttentionSubmit), protocol.InputPermissionRequired)
	assertInputProjection(t, reducer.ApplyAttentionAction(protocol.TerminalAttentionCancel), protocol.InputNone)
	if projection := reducer.ApplyAttentionAction(protocol.TerminalAttentionInterrupt); projection.InputChanged {
		t.Fatalf("attention action completed an idle reducer: %#v", projection)
	}
}

func TestReducerReopensARealLaterPermissionInTheSameTurn(t *testing.T) {
	reducer := startedReducer(t)
	permission := NormalizedEvent{Event: "PermissionRequest", SessionID: reducerSessionID, ToolName: "Bash"}
	assertInputProjection(t, reducer.Apply(permission), protocol.InputPermissionRequired)
	assertInputProjection(t, reducer.ApplyAttentionAction(protocol.TerminalAttentionSubmit), protocol.InputNone)
	assertInputProjection(t, reducer.Apply(permission), protocol.InputPermissionRequired)
	assertInputProjection(t, reducer.Apply(NormalizedEvent{
		Event: "PermissionDenied", SessionID: reducerSessionID, ToolName: "Bash",
	}), protocol.InputNone)
}

func startedReducer(t *testing.T) *Reducer {
	t.Helper()
	reducer := NewReducer(protocol.InputNone)
	projection := reducer.Apply(NormalizedEvent{Event: "SessionStart", SessionID: reducerSessionID})
	if !projection.Metadata {
		t.Fatalf("startup projection = %#v", projection)
	}
	return reducer
}

func assertInputProjection(t *testing.T, projection Projection, state protocol.InputState) {
	t.Helper()
	if !projection.InputChanged || projection.InputState != state {
		t.Fatalf("projection = %#v, want input %q", projection, state)
	}
}
