package agentsessions

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminals"
)

type fakeDriver struct {
	started bool
	resumed bool
}

type prepareFailDriver struct {
	fakeDriver
	err error
}

func (driver *prepareFailDriver) PrepareMonitor(string) (harness.MonitorRuntime, error) {
	return nil, driver.err
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(value)
}

func (buffer *synchronizedBuffer) Reset() {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.buffer.Reset()
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func (*fakeDriver) Probe(context.Context) protocol.HarnessCapability {
	return protocol.HarnessCapability{Type: protocol.HarnessCodex, Available: true, Version: "test"}
}
func (d *fakeDriver) Command(spec harness.StartSpec) (*exec.Cmd, error) {
	d.started = true
	cmd := exec.Command("fake-agent", "start")
	cmd.Dir = spec.RepositoryPath
	return cmd, nil
}
func (d *fakeDriver) ResumeCommand(harness.ResumeSpec) (*exec.Cmd, error) {
	d.resumed = true
	return exec.Command("fake-agent", "resume"), nil
}
func (*fakeDriver) ForkCommand(harness.ForkSpec) (*exec.Cmd, error) {
	return exec.Command("fake-agent", "fork"), nil
}
func (*fakeDriver) Monitor(context.Context, harness.MonitorSpec, func(harness.Event)) {}

type fakeState struct {
	session   Session
	bound     string
	bindCalls int
	running   bool
	failed    string
	generated chan Proposal
	applyErr  error
}

func (s *fakeState) Session(context.Context, string, string) (Session, error) { return s.session, nil }
func (s *fakeState) Bind(_ context.Context, _, _, v string) error {
	s.bindCalls++
	s.bound = v
	return nil
}
func (s *fakeState) Running(context.Context, string, string) error               { s.running = true; return nil }
func (*fakeState) Observed(context.Context, string, string, harness.Event) error { return nil }
func (s *fakeState) Failed(_ context.Context, _, _, reason string) error {
	s.failed = reason
	return nil
}
func (s *fakeState) ApplyGeneratedIdentity(_ context.Context, _, _, title, slug string) error {
	if s.generated != nil {
		s.generated <- Proposal{Slug: slug, Title: title}
	}
	return s.applyErr
}

type fakeNamer struct {
	err     error
	options chan LaunchOptions
}

func (n fakeNamer) Name(_ context.Context, _ Session, options LaunchOptions) (Proposal, error) {
	if n.options != nil {
		n.options <- options
	}
	return Proposal{Slug: "fix-tests", Title: "Fix tests"}, n.err
}

type fakeLauncher struct {
	spec        terminals.LaunchSpec
	launchCalls int
}

func (l *fakeLauncher) Launch(_ context.Context, s terminals.LaunchSpec) (int, error) {
	l.launchCalls++
	l.spec = s
	return 42, nil
}

func TestLaunchAppliesAsynchronousGeneratedIdentity(t *testing.T) {
	state := &fakeState{session: Session{HolonID: "h", ID: "a", Worktree: t.TempDir(), AgentType: "codex", Title: "Fix", Prompt: "fix", Initial: true}, generated: make(chan Proposal, 1)}
	service, _ := New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{protocol.HarnessCodex: &fakeDriver{}}), state, &fakeLauncher{}, nil, fakeNamer{}, t.TempDir())
	options := LaunchOptions{GenerateIdentity: true, GenerateTitle: true}
	if err := service.Launch(t.Context(), "h", "a", terminals.Dimensions{Columns: 80, Rows: 24}, "", options); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-state.generated:
		if got.Slug != "fix-tests" || got.Title != "Fix tests" {
			t.Fatalf("proposal=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("generated identity not applied")
	}
}

func TestLaunchPreservesTitleWhileApplyingGeneratedBranch(t *testing.T) {
	state := &fakeState{session: Session{HolonID: "h", ID: "a", Worktree: t.TempDir(), AgentType: "codex", Title: "User title", Prompt: "fix", Initial: true}, generated: make(chan Proposal, 1)}
	service, _ := New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{protocol.HarnessCodex: &fakeDriver{}}), state, &fakeLauncher{}, nil, fakeNamer{}, t.TempDir())
	if err := service.Launch(t.Context(), "h", "a", terminals.Dimensions{Columns: 80, Rows: 24}, "", LaunchOptions{GenerateIdentity: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-state.generated:
		if got.Slug != "fix-tests" || got.Title != "" {
			t.Fatalf("proposal applied=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("generated branch not applied")
	}
}

func TestNamingFailuresAreBestEffortAfterAgentLaunch(t *testing.T) {
	var logs synchronizedBuffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })
	for _, test := range []struct {
		name     string
		namerErr error
		applyErr error
	}{
		{name: "namer failure", namerErr: errors.New("namer unavailable")},
		{name: "identity application failure", applyErr: errors.New("rename failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			called := make(chan LaunchOptions, 1)
			state := &fakeState{session: Session{HolonID: "holon-1", ID: "agent-1", Worktree: t.TempDir(), AgentType: "codex", Title: "Fallback", Prompt: "fix", Initial: true}, generated: make(chan Proposal, 1), applyErr: test.applyErr}
			service, _ := New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{protocol.HarnessCodex: &fakeDriver{}}), state, &fakeLauncher{}, nil, fakeNamer{err: test.namerErr, options: called}, t.TempDir())
			if err := service.Launch(t.Context(), "holon-1", "agent-1", terminals.Dimensions{Columns: 80, Rows: 24}, "", LaunchOptions{GenerateIdentity: true, GenerateTitle: true}); err != nil {
				t.Fatalf("launch failed because naming failed: %v", err)
			}
			select {
			case options := <-called:
				if !options.GenerateTitle {
					t.Fatal("title policy was not passed to namer")
				}
			case <-time.After(time.Second):
				t.Fatal("namer was not called")
			}
			if !state.running {
				t.Fatal("agent did not remain running")
			}
			deadline := time.Now().Add(time.Second)
			for !strings.Contains(logs.String(), "holon_id=holon-1 agent_id=agent-1") && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !strings.Contains(logs.String(), "holon_id=holon-1 agent_id=agent-1") {
				t.Fatalf("failure log missing identifiers: %q", logs.String())
			}
		})
	}
}

func TestZeroValueLaunchOptionsDoNotRerunIdentityNaming(t *testing.T) {
	called := make(chan LaunchOptions, 1)
	state := &fakeState{session: Session{HolonID: "h", ID: "a", Worktree: t.TempDir(), AgentType: "codex", Title: "Existing", Prompt: "fix", Initial: true}}
	service, _ := New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{protocol.HarnessCodex: &fakeDriver{}}), state, &fakeLauncher{}, nil, fakeNamer{options: called}, t.TempDir())
	if err := service.Launch(t.Context(), "h", "a", terminals.Dimensions{Columns: 80, Rows: 24}, "", LaunchOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
		t.Fatal("zero-value launch options reran identity naming")
	case <-time.After(20 * time.Millisecond):
	}
}
func (*fakeLauncher) Signal(context.Context, terminals.TerminalID) error        { return nil }
func (*fakeLauncher) Input(context.Context, terminals.TerminalID, []byte) error { return nil }

func TestLaunchBindsDirectLocalPTYAndProjectsRunning(t *testing.T) {
	driver := &fakeDriver{}
	state := &fakeState{session: Session{HolonID: "holon-1", ID: "agent-1", Worktree: t.TempDir(), AgentType: "codex", Prompt: "fix"}}
	launcher := &fakeLauncher{}
	service, err := New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{protocol.HarnessCodex: driver}), state, launcher, nil, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Launch(t.Context(), "holon-1", "agent-1", terminals.Dimensions{Columns: 80, Rows: 24}, "", LaunchOptions{}); err != nil {
		t.Fatal(err)
	}
	if state.bound == "" || !state.running || launcher.spec.Command != "fake-agent" || launcher.spec.CWD != state.session.Worktree {
		t.Fatalf("state=%+v spec=%+v", state, launcher.spec)
	}
}

func TestLaunchDoesNotBindWhenMonitorPreparationFails(t *testing.T) {
	prepareErr := errors.New("monitor unavailable")
	driver := &prepareFailDriver{err: prepareErr}
	state := &fakeState{session: Session{HolonID: "holon-1", ID: "agent-1", Worktree: t.TempDir(), AgentType: "codex", Prompt: "fix"}}
	launcher := &fakeLauncher{}
	service, err := New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{protocol.HarnessCodex: driver}), state, launcher, nil, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = service.Launch(t.Context(), "holon-1", "agent-1", terminals.Dimensions{Columns: 80, Rows: 24}, "", LaunchOptions{})
	if !errors.Is(err, prepareErr) {
		t.Fatalf("launch error = %v, want monitor preparation error", err)
	}
	if state.bindCalls != 0 || state.bound != "" || launcher.launchCalls != 0 || state.running {
		t.Fatalf("state=%+v launcher=%+v", state, launcher)
	}
	if state.failed == "" {
		t.Fatal("monitor preparation failure was not persisted")
	}
}

func TestLaunchUsesHarnessResumeCommand(t *testing.T) {
	driver := &fakeDriver{}
	state := &fakeState{session: Session{HolonID: "holon-1", ID: "agent-1", Worktree: t.TempDir(), AgentType: "codex", Prompt: "fix", ResumeTarget: "resume-1"}}
	service, _ := New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{protocol.HarnessCodex: driver}), state, &fakeLauncher{}, nil, nil, t.TempDir())
	if err := service.Launch(t.Context(), "holon-1", "agent-1", terminals.Dimensions{Columns: 80, Rows: 24}, "continue", LaunchOptions{}); err != nil {
		t.Fatal(err)
	}
	if !driver.resumed {
		t.Fatal("resume command was not used")
	}
}

func TestLaunchRequiresSavedConversationBeforeStartingAgent(t *testing.T) {
	driver := &fakeDriver{}
	state := &fakeState{session: Session{HolonID: "holon-1", ID: "agent-1", Worktree: t.TempDir(), AgentType: "codex", Prompt: "fix"}}
	launcher := &fakeLauncher{}
	service, err := New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{protocol.HarnessCodex: driver}), state, launcher, nil, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = service.Launch(t.Context(), "holon-1", "agent-1", terminals.Dimensions{Columns: 80, Rows: 24}, "continue", LaunchOptions{RequireResume: true})
	if err == nil || !strings.Contains(err.Error(), "No saved conversation is available to restore") {
		t.Fatalf("launch error = %v", err)
	}
	if driver.started || driver.resumed || launcher.launchCalls != 0 {
		t.Fatalf("agent started without a saved conversation: driver=%+v launch calls=%d", driver, launcher.launchCalls)
	}
	if state.failed == "" {
		t.Fatal("unavailable conversation was not persisted as a launch failure")
	}
}
