package harness

import (
	"context"
	"io/fs"
	"os/exec"
	"testing"

	"github.com/holark-ai/holark/internal/harness/codex"
	"github.com/holark-ai/holark/internal/protocol"
)

func TestRegistryFiltersNilDriversAndDispatchesHarnessSpecs(t *testing.T) {
	driver := &recordingDriver{capability: protocol.HarnessCapability{Type: protocol.HarnessType("fixture"), Available: true, Version: "fixture-1"}}
	registry := NewRegistry(map[protocol.HarnessType]Driver{
		protocol.HarnessType("fixture"): driver,
		protocol.HarnessType("nil"):     nil,
	})

	capabilities := registry.Probe(context.Background())
	if len(capabilities) != 1 || capabilities[0].Type != protocol.HarnessType("fixture") || !capabilities[0].Available {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if _, ok := registry.Driver(protocol.HarnessType("nil")); ok {
		t.Fatal("nil driver was registered")
	}
	selected, ok := registry.Driver(protocol.HarnessType("fixture"))
	if !ok || selected != driver {
		t.Fatalf("selected driver = %+v, %v", selected, ok)
	}

	harnessSession := protocol.HarnessSession{ID: "harness-session-1", HarnessType: protocol.HarnessType("fixture"), ResumeTarget: "resume-1", InputState: protocol.InputNone}
	if _, err := selected.Command(StartSpec{RepositoryPath: "/tmp/repo", SessionID: "session-1", Prompt: "work", HarnessSession: harnessSession}); err != nil {
		t.Fatal(err)
	}
	if driver.startSpec.HarnessSession.ID != harnessSession.ID || driver.startSpec.Prompt != "work" {
		t.Fatalf("start spec = %+v", driver.startSpec)
	}
	if _, err := selected.ResumeCommand(ResumeSpec{RepositoryPath: "/tmp/repo", SessionID: "session-1", HarnessSession: harnessSession}); err != nil {
		t.Fatal(err)
	}
	if driver.resumeSpec.HarnessSession.ResumeTarget != "resume-1" {
		t.Fatalf("resume spec = %+v", driver.resumeSpec)
	}

	var events []Event
	selected.Monitor(context.Background(), MonitorSpec{Worktree: "/tmp/repo", HarnessSession: harnessSession}, func(value Event) {
		events = append(events, value)
	})
	if driver.monitorSpec.HarnessSession.ID != harnessSession.ID || len(events) != 2 || events[0].HarnessSessionID != harnessSession.ID || events[0].ResumeTarget != "resume-1" || events[1].InputState != protocol.InputUserRequired {
		t.Fatalf("monitor spec = %+v events = %+v", driver.monitorSpec, events)
	}
}

func TestCodexDriverSkillInstallSpec(t *testing.T) {
	spec, ok := codexDriver{}.SkillInstall()
	if !ok {
		t.Fatal("Codex driver did not declare a skill install")
	}
	if spec.TargetDir != ".agents/skills/holark-integration" || len(spec.Sources) != 2 {
		t.Fatalf("Codex skill install = %+v", spec)
	}
	assertSourceFile(t, spec.Sources[0], "SKILL.md")
	assertSourceFile(t, spec.Sources[1], "agents/openai.yaml")
}

func TestClaudeDriverSkillInstallSpec(t *testing.T) {
	spec, ok := claudeDriver{}.SkillInstall()
	if !ok {
		t.Fatal("Claude driver did not declare a skill install")
	}
	if spec.TargetDir != ".claude/skills/holark-integration" || len(spec.Sources) != 1 {
		t.Fatalf("Claude skill install = %+v", spec)
	}
	assertSourceFile(t, spec.Sources[0], "SKILL.md")
}

func TestClaudeDriverRequiresPrivateRuntime(t *testing.T) {
	driver, ok := any(claudeDriver{}).(PrivateRuntimeDriver)
	if !ok {
		t.Fatal("Claude driver did not advertise private runtime support")
	}
	if !driver.RequiresPrivateRuntime() {
		t.Fatal("Claude driver did not require a private runtime")
	}
	if _, ok := any(codexDriver{}).(PrivateRuntimeDriver); ok {
		t.Fatal("Codex driver unexpectedly advertised private runtime support")
	}
}

func assertSourceFile(t *testing.T, source SkillSource, relativePath string) {
	t.Helper()
	data, err := fs.ReadFile(source.FS, source.Root+"/"+relativePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatalf("source file %s is empty", relativePath)
	}
}

func TestCodexDriverResumeCommandUsesHarnessResumeTarget(t *testing.T) {
	driver := codexDriver{manager: commandServer{}}
	repository := t.TempDir()
	command, err := driver.ResumeCommand(ResumeSpec{
		RepositoryPath: repository,
		SessionID:      "session-1",
		HarnessSession: protocol.HarnessSession{ID: "harness-session-1", HarnessType: protocol.HarnessCodex, ResumeTarget: "codex-session-1", InputState: protocol.InputNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(command.Args) == 0 || command.Args[len(command.Args)-1] != "codex-session-1" {
		t.Fatalf("resume args = %+v", command.Args)
	}
}

func TestCodexDriverResumeCommandRestartsHarnessWithoutResumeTarget(t *testing.T) {
	driver := codexDriver{manager: commandServer{}}
	repository := t.TempDir()
	command, err := driver.ResumeCommand(ResumeSpec{
		RepositoryPath: repository,
		SessionID:      "session-1",
		FollowUpPrompt: "new task",
		HarnessSession: protocol.HarnessSession{ID: "harness-session-1", HarnessType: protocol.HarnessCodex, Prompt: "original task", InputState: protocol.InputTaskComplete},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(command.Args) < 2 || command.Args[1] != "--cd" {
		t.Fatalf("restart args = %+v, want a fresh interactive Codex command", command.Args)
	}
	for _, argument := range command.Args[1:] {
		if argument == "resume" {
			t.Fatalf("restart args = %+v, unexpectedly resumed a missing target", command.Args)
		}
	}
	if command.Args[len(command.Args)-1] != "new task" {
		t.Fatalf("restart args = %+v, want follow-up prompt", command.Args)
	}
}

type recordingDriver struct {
	capability  protocol.HarnessCapability
	startSpec   StartSpec
	resumeSpec  ResumeSpec
	forkSpec    ForkSpec
	monitorSpec MonitorSpec
}

func (driver *recordingDriver) Probe(context.Context) protocol.HarnessCapability {
	return driver.capability
}

func (driver *recordingDriver) Command(spec StartSpec) (*exec.Cmd, error) {
	driver.startSpec = spec
	return exec.Command("holark-fixture"), nil
}

func (driver *recordingDriver) ResumeCommand(spec ResumeSpec) (*exec.Cmd, error) {
	driver.resumeSpec = spec
	return exec.Command("holark-fixture"), nil
}

func (driver *recordingDriver) ForkCommand(spec ForkSpec) (*exec.Cmd, error) {
	driver.forkSpec = spec
	return exec.Command("holark-fixture"), nil
}

func (driver *recordingDriver) Monitor(_ context.Context, spec MonitorSpec, send func(Event)) {
	driver.monitorSpec = spec
	if send == nil {
		return
	}
	send(Event{Type: EventSessionDiscovered, HarnessSessionID: spec.HarnessSession.ID, HarnessType: spec.HarnessSession.HarnessType, ResumeTarget: spec.HarnessSession.ResumeTarget, RolloutPath: spec.HarnessSession.RolloutPath})
	send(Event{Type: EventInputStateChanged, HarnessSessionID: spec.HarnessSession.ID, HarnessType: spec.HarnessSession.HarnessType, InputState: protocol.InputUserRequired})
}

// Command tests exercise the runtime port without executing or emulating a CLI.
type commandServer struct{}

func (commandServer) Prepare(context.Context, *exec.Cmd, protocol.HarnessSession) error { return nil }
func (commandServer) Monitor(context.Context, string, func(codex.Observation))          {}
func (commandServer) Stop(context.Context, string) error                                { return nil }
