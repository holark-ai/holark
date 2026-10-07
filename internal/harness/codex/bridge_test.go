package codex

import (
	"context"
	"fmt"
	"testing"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestCanBindThreadOnlyAcceptsCurrentThreadForks(t *testing.T) {
	tests := []struct {
		name, method, previous, startSource string
		thread                              threadSnapshot
		want                                bool
	}{
		{"initial fork", "thread/fork", "", "", threadSnapshot{ID: "first", ForkedFromID: "source"}, true},
		{"same thread resume", "thread/resume", "first", "", threadSnapshot{ID: "first"}, true},
		{"explicit clear", "thread/start", "first", "clear", threadSnapshot{ID: "new"}, true},
		{"direct successor", "thread/fork", "first", "", threadSnapshot{ID: "second", ForkedFromID: "first"}, true},
		{"unrelated fork", "thread/fork", "first", "", threadSnapshot{ID: "other", ForkedFromID: "unrelated"}, false},
		{"sibling fork", "thread/fork", "first", "", threadSnapshot{ID: "sibling", ForkedFromID: "source"}, false},
		{"non-fork response", "thread/resume", "first", "", threadSnapshot{ID: "second", ForkedFromID: "first"}, false},
		{"subagent", "thread/fork", "first", "", threadSnapshot{ID: "child", ForkedFromID: "first", Parent: "first"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canBindThread(tt.method, tt.previous, tt.startSource, tt.thread); got != tt.want {
				t.Fatalf("canBindThread(%q, %q, %q, %+v) = %t, want %t", tt.method, tt.previous, tt.startSource, tt.thread, got, tt.want)
			}
		})
	}
}

func TestReconcileDeliversNewTurnCompletionWhenDisplayStateIsUnchanged(t *testing.T) {
	reducer := NewReducer("thread-one", 1, protocol.InputNone)
	apply := func(f Fact) {
		f.ThreadID = "thread-one"
		f.Launch = 1
		f.Connection = 1
		reducer.Apply(f)
	}
	apply(Fact{Kind: "turn_started", TurnID: "turn-one"})
	apply(Fact{Kind: "turn_completed", TurnID: "turn-one", Status: "completed"})

	var observations []Observation
	binding := &launchBinding{
		manager:    &Manager{},
		ctx:        context.Background(),
		generation: 1,
		connection: 1,
		reducer:    reducer,
		sink: func(observation Observation) {
			observations = append(observations, observation)
		},
	}
	binding.lockReduction()
	binding.reconcile(threadSnapshot{ID: "thread-one", Turns: []turnSnapshot{
		{ID: "turn-one", Status: "completed"},
		{ID: "turn-two", Status: "completed"},
	}})
	binding.unlockReduction()

	if len(observations) != 2 {
		t.Fatalf("observations = %#v, want a rearm and completion for the new turn", observations)
	}
	if observations[0].TurnID != "turn-two" || observations[0].Input != protocol.InputNone || observations[0].Activity != protocol.ActivityWorking {
		t.Fatalf("rearm observation = %#v", observations[0])
	}
	if observations[1].TurnID != "turn-two" || observations[1].Input != protocol.InputTaskComplete || observations[1].Activity != protocol.ActivityCompleted {
		t.Fatalf("completion observation = %#v", observations[1])
	}
}

func TestTokenUsageNotificationPublishesCurrentMainThreadContext(t *testing.T) {
	var observations []Observation
	binding := &launchBinding{
		manager:    &Manager{},
		ctx:        context.Background(),
		generation: 1,
		connection: 1,
		reducer:    NewReducer("thread-one", 1, protocol.InputNone),
		sink: func(observation Observation) {
			observations = append(observations, observation)
			if observation.Acknowledge != nil {
				observation.Acknowledge(nil)
			}
		},
	}
	current := func(thread string, last int64) rpcMessage {
		return rpcMessage{Method: "thread/tokenUsage/updated", Params: []byte(`{"threadId":"` + thread + `","tokenUsage":{"total":{"totalTokens":28805},"last":{"totalTokens":` + fmt.Sprint(last) + `},"modelContextWindow":258400}}`)}
	}

	binding.lockReduction()
	binding.reduceMessage(current("thread-one", 14470))
	binding.reduceMessage(current("thread-one", 14470))
	binding.reduceMessage(current("other-thread", 15159))
	binding.reduceMessage(current("thread-one", -1))
	binding.unlockReduction()

	if len(observations) != 1 || observations[0].ContextTokens == nil || *observations[0].ContextTokens != 14470 {
		t.Fatalf("context observations = %#v", observations)
	}

	binding.lockReduction()
	err := binding.apply(Fact{Kind: "binding", ThreadID: "thread-two", Predecessor: "thread-one", Verified: true})
	binding.reduceMessage(current("thread-one", 15159))
	binding.reduceMessage(current("thread-two", 14470))
	binding.reduceMessage(current("thread-two", 14470))
	binding.unlockReduction()
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 3 || observations[1].Identity != "thread-two" || observations[2].ContextTokens == nil || *observations[2].ContextTokens != 14470 {
		t.Fatalf("context observations after switching threads = %#v", observations)
	}
}
