package codex

import (
	"github.com/holark-ai/holark/internal/protocol"
	"testing"
)

func TestReducerInputState(t *testing.T) {
	r := NewReducer("main", 1, protocol.InputNone)
	apply := func(f Fact) Projection { f.ThreadID = "main"; f.Launch = 1; f.Connection = 1; return r.Apply(f) }
	check := func(want protocol.InputState) {
		t.Helper()
		if r.Input != want {
			t.Fatalf("input=%s want=%s", r.Input, want)
		}
	}
	apply(Fact{Kind: "settings", Mode: "plan"})
	apply(Fact{Kind: "turn_started", TurnID: "one"})
	apply(Fact{Kind: "permission", TurnID: "one", RequestID: "approval"})
	check(protocol.InputPermissionRequired)
	apply(Fact{Kind: "question", TurnID: "one", RequestID: "question"})
	check(protocol.InputUserRequired)
	apply(Fact{Kind: "automatic_review_settings"})
	check(protocol.InputUserRequired)
	apply(Fact{Kind: "resolved", RequestID: "question"})
	check(protocol.InputPermissionRequired)
	apply(Fact{Kind: "automatic_review"})
	check(protocol.InputPermissionRequired)
	apply(Fact{Kind: "resolved", RequestID: "approval"})
	check(protocol.InputNone)
	apply(Fact{Kind: "plan_completed", TurnID: "one", Plan: true})
	apply(Fact{Kind: "turn_completed", TurnID: "one", Status: "completed"})
	check(protocol.InputUserRequired)
	if p := apply(Fact{Kind: "turn_completed", TurnID: "one", Status: "completed"}); p.Changed {
		t.Fatal("duplicate plan completion")
	}
	apply(Fact{Kind: "turn_started", TurnID: "two"})
	check(protocol.InputNone)
	apply(Fact{Kind: "plan_completed", TurnID: "two", Plan: false})
	apply(Fact{Kind: "turn_completed", TurnID: "two", Status: "completed"})
	check(protocol.InputTaskComplete)
	apply(Fact{Kind: "settings", Mode: "default"})
	apply(Fact{Kind: "turn_started", TurnID: "default-plan"})
	apply(Fact{Kind: "plan_completed", TurnID: "default-plan", Plan: true})
	apply(Fact{Kind: "turn_completed", TurnID: "default-plan", Status: "completed"})
	check(protocol.InputTaskComplete)
	for _, status := range []string{"interrupted", "failed"} {
		apply(Fact{Kind: "turn_started", TurnID: status})
		apply(Fact{Kind: "question", TurnID: status, RequestID: status})
		apply(Fact{Kind: "turn_completed", TurnID: status, Status: status})
		check(protocol.InputNone)
	}
	apply(Fact{Kind: "idle"})
	check(protocol.InputNone)
}
func TestReducerOwnership(t *testing.T) {
	r := NewReducer("main", 1, protocol.InputNone)
	for _, id := range []string{"worker", "guardian", "peer", "fork"} {
		if p := r.Apply(Fact{Kind: "binding", ThreadID: id, Launch: 1, Connection: 1}); p.Identity != "" {
			t.Fatalf("unverified identity %s", id)
		}
		r.Apply(Fact{Kind: "question", ThreadID: id, RequestID: id, Launch: 1, Connection: 1})
	}
	if r.ThreadID != "main" || r.Input != protocol.InputNone {
		t.Fatalf("family replaced main: %+v", r)
	}
	p := r.Apply(Fact{Kind: "binding", ThreadID: "successor", Predecessor: "main", Verified: true, Launch: 1, Connection: 1})
	if p.Identity != "successor" {
		t.Fatal("verified successor was not persisted")
	}
	r.Apply(Fact{Kind: "binding", ThreadID: "unrelated", Predecessor: "main", Verified: true, Launch: 1, Connection: 1})
	if r.ThreadID != "successor" {
		t.Fatal("obsolete predecessor replaced successor")
	}
}
func TestReducerRecovery(t *testing.T) {
	r := NewReducer("main", 2, protocol.InputNone)
	apply := func(f Fact) Projection { f.ThreadID = "main"; f.Launch = 2; f.Connection = 3; return r.Apply(f) }
	apply(Fact{TurnID: "imported", Baseline: true})
	if p := apply(Fact{Kind: "turn_completed", TurnID: "imported", Status: "completed"}); p.Changed {
		t.Fatal("imported history completed launch")
	}
	apply(Fact{Kind: "turn_started", TurnID: "live"})
	if p := apply(Fact{Kind: "turn_completed", TurnID: "live", Status: "completed"}); !p.Changed {
		t.Fatal("completion missing")
	}
	if p := apply(Fact{Kind: "turn_completed", TurnID: "live", Status: "completed"}); p.Changed {
		t.Fatal("duplicate completion")
	}
	apply(Fact{Kind: "turn_started", TurnID: "next"})
	apply(Fact{Kind: "resolved", RequestID: "resolved"})
	apply(Fact{Kind: "permission", TurnID: "next", RequestID: "resolved"})
	apply(Fact{Kind: "question", TurnID: "live", RequestID: "stopped"})
	if r.Input != protocol.InputNone {
		t.Fatal("obsolete pending request was replayed")
	}
	apply(Fact{Kind: "question", TurnID: "next", RequestID: "pending"})
	if r.Input != protocol.InputUserRequired {
		t.Fatal("live pending request was lost")
	}
	for _, fact := range []Fact{
		{Kind: "resolved", ThreadID: "main", RequestID: "pending", Launch: 1, Connection: 4},
		{Kind: "resolved", ThreadID: "main", RequestID: "pending", Launch: 2, Connection: 2},
		{Kind: "binding", ThreadID: "old", Predecessor: "main", Verified: true, Launch: 1, Connection: 4},
	} {
		r.Apply(fact)
	}
	if r.ThreadID != "main" || r.Input != protocol.InputUserRequired {
		t.Fatal("stale generation changed state")
	}
	// Ordered recovery can reveal a whole newer turn that was missed live.
	apply(Fact{Kind: "turn_completed", TurnID: "next", Status: "interrupted"})
	apply(Fact{Kind: "turn_started", TurnID: "missed"})
	if p := apply(Fact{Kind: "turn_completed", TurnID: "missed", Status: "completed"}); !p.Changed || p.Input != protocol.InputTaskComplete {
		t.Fatal("entirely missed turn did not recover completion")
	}
	if p := apply(Fact{Kind: "turn_started", TurnID: "missed"}); p.Changed {
		t.Fatal("replayed start cleared a completed turn")
	}
}
