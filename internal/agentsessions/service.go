// Package agentsessions owns local coding-agent launch and observation.
package agentsessions

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminals"
)

var ErrUnavailable = errors.New("agent harness is unavailable")

type Session struct {
	Permissions                                                                            string
	Model                                                                                  string
	Restoring                                                                              bool
	HolonID, ID, TerminalID, Worktree, AgentType, Title, Prompt, ResumeTarget, RolloutPath string
	Activity                                                                               protocol.AgentActivity
	InputState                                                                             protocol.InputState
	Initial                                                                                bool
}

type State interface {
	Session(context.Context, string, string) (Session, error)
	Bind(context.Context, string, string, string) error
	Running(context.Context, string, string) error
	Observed(context.Context, string, string, harness.Event) error
	Failed(context.Context, string, string, string) error
	ApplyGeneratedIdentity(context.Context, string, string, string, string) error
}

type Launcher interface {
	Launch(context.Context, terminals.LaunchSpec) (int, error)
	Signal(context.Context, terminals.TerminalID) error
	Input(context.Context, terminals.TerminalID, []byte) error
}
type SkillInstaller interface {
	Install(context.Context, string, harness.SkillInstallSpec) error
}
type Proposal struct{ Slug, Title string }
type LaunchOptions struct {
	RequireResume    bool
	GenerateIdentity bool
	GenerateTitle    bool
}
type Namer interface {
	Name(context.Context, Session, LaunchOptions) (Proposal, error)
}

type Service struct {
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	monitors    map[string]executionMonitor
	registry    *harness.Registry
	state       State
	launcher    Launcher
	installer   SkillInstaller
	namer       Namer
	runtimeRoot string
}

type executionMonitor struct {
	terminal string
	driver   harness.Driver
	cancel   context.CancelFunc
	actions  chan protocol.TerminalAttentionAction
}

func New(registry *harness.Registry, state State, launcher Launcher, installer SkillInstaller, namer Namer, runtimeRoot string) (*Service, error) {
	if registry == nil || state == nil || launcher == nil || runtimeRoot == "" {
		return nil, errors.New("agent session dependencies are required")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &Service{ctx: lifetime, cancel: cancel, monitors: map[string]executionMonitor{}, registry: registry, state: state, launcher: launcher, installer: installer, namer: namer, runtimeRoot: runtimeRoot}, nil
}

func (s *Service) Capabilities(ctx context.Context) []protocol.HarnessCapability {
	return s.registry.Probe(ctx)
}

func (s *Service) Launch(ctx context.Context, holonID, agentID string, dimensions terminals.Dimensions, followUp string, options LaunchOptions) (err error) {
	session, err := s.state.Session(ctx, holonID, agentID)
	if err != nil {
		return fmt.Errorf("load agent session: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.state.Failed(context.WithoutCancel(ctx), holonID, agentID, err.Error()))
		}
	}()
	if options.RequireResume && session.ResumeTarget == "" {
		return errors.New("No saved conversation is available to restore.")
	}
	driver, ps, runtimeDir, err := s.prepare(ctx, session)
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	if session.ResumeTarget != "" {
		cmd, err = driver.ResumeCommand(harness.ResumeSpec{RequireExisting: options.RequireResume, RepositoryPath: session.Worktree, SessionID: holonID, FollowUpPrompt: followUp, HarnessSession: ps, RuntimeDir: runtimeDir})
	} else {
		cmd, err = driver.Command(harness.StartSpec{RepositoryPath: session.Worktree, SessionID: holonID, Prompt: session.Prompt, HarnessSession: ps, RuntimeDir: runtimeDir})
	}
	if err != nil {
		return fmt.Errorf("build agent command (resume=%t): %w", session.ResumeTarget != "", err)
	}
	if err = s.launch(ctx, session, driver, ps, runtimeDir, cmd, dimensions); err != nil {
		return err
	}
	if options.GenerateIdentity && session.Initial && session.ResumeTarget == "" && s.namer != nil {
		s.GenerateIdentity(session, options)
	}
	return nil
}

// GenerateIdentity names a Holon asynchronously, independently of its startup resource.
func (s *Service) GenerateIdentity(session Session, options LaunchOptions) {
	if s.namer == nil {
		return
	}
	go func() {
		proposal, namingErr := s.namer.Name(context.Background(), session, options)
		if namingErr != nil {
			log.Printf("holon naming failed holon_id=%s agent_id=%s: %v", session.HolonID, session.ID, namingErr)
			return
		}
		if proposal.Slug == "" {
			log.Printf("holon naming failed holon_id=%s agent_id=%s: empty generated branch slug", session.HolonID, session.ID)
			return
		}
		generatedTitle := ""
		if options.GenerateTitle {
			generatedTitle = proposal.Title
		}
		if applyErr := s.state.ApplyGeneratedIdentity(context.Background(), session.HolonID, session.Title, generatedTitle, proposal.Slug); applyErr != nil {
			log.Printf("apply generated holon identity failed holon_id=%s agent_id=%s: %v", session.HolonID, session.ID, applyErr)
		}
	}()
}

// Fork launches a new conversation owned by an already persisted destination.
// Source conversation metadata is used only to construct the native fork command.
func (s *Service) Fork(ctx context.Context, holonID, sourceAgentID, destinationAgentID string, dimensions terminals.Dimensions) (err error) {
	if sourceAgentID == destinationAgentID {
		return errors.New("fork source and destination must be different agents")
	}
	destination, err := s.state.Session(ctx, holonID, destinationAgentID)
	if err != nil {
		return err
	}
	if destination.HolonID != holonID || destination.ID != destinationAgentID {
		return errors.New("fork destination does not belong to this Holon")
	}
	if destination.TerminalID != "" || destination.ResumeTarget != "" || destination.RolloutPath != "" {
		return errors.New("fork destination already owns a conversation or terminal")
	}
	defer func() {
		if err != nil {
			// A cancelled launch request must still leave its durable tab visibly failed.
			err = errors.Join(err, s.state.Failed(context.WithoutCancel(ctx), holonID, destinationAgentID, err.Error()))
		}
	}()
	source, err := s.state.Session(ctx, holonID, sourceAgentID)
	if err != nil {
		return err
	}
	if source.HolonID != holonID || source.ID != sourceAgentID {
		return errors.New("fork source does not belong to this Holon")
	}
	if source.AgentType != destination.AgentType {
		return errors.New("fork source and destination must use the same harness")
	}
	driver, ps, runtimeDir, err := s.prepare(ctx, destination)
	if err != nil {
		return err
	}
	cmd, err := driver.ForkCommand(harness.ForkSpec{
		RepositoryPath: destination.Worktree, SessionID: holonID, Prompt: destination.Prompt,
		SourceResumeTarget: source.ResumeTarget, SourceRolloutPath: source.RolloutPath,
		HarnessSession: ps, RuntimeDir: runtimeDir,
	})
	if err != nil {
		return err
	}
	return s.launch(ctx, destination, driver, ps, runtimeDir, cmd, dimensions)
}

func (s *Service) prepare(ctx context.Context, session Session) (harness.Driver, protocol.HarnessSession, string, error) {
	var ps protocol.HarnessSession
	driver, ok := s.registry.Driver(protocol.HarnessType(session.AgentType))
	if !ok || !driver.Probe(ctx).Available {
		return nil, ps, "", fmt.Errorf("resolve or probe harness %q: %w", session.AgentType, ErrUnavailable)
	}
	if source, ok := driver.(harness.SkillInstaller); ok && s.installer != nil {
		if install, enabled := source.SkillInstall(); enabled {
			if err := s.installer.Install(ctx, session.Worktree, install); err != nil {
				return nil, ps, "", fmt.Errorf("install agent skills: %w", err)
			}
		}
	}
	tid, err := terminals.NewID()
	if err != nil {
		return nil, ps, "", err
	}
	runtimeDir := ""
	if private, ok := driver.(harness.PrivateRuntimeDriver); ok && private.RequiresPrivateRuntime() {
		agentDir := filepath.Join(s.runtimeRoot, session.ID)
		if err := os.MkdirAll(agentDir, 0o700); err != nil {
			return nil, ps, "", fmt.Errorf("create agent runtime directory: %w", err)
		}
		// Observer files belong to a process launch, not the durable agent.
		// Keep previous runs intact so their events cannot leak into a resume.
		runtimeDir = filepath.Join(agentDir, string(tid))
		if err := os.Mkdir(runtimeDir, 0o700); err != nil {
			return nil, ps, "", fmt.Errorf("create agent runtime directory: %w", err)
		}
	}
	ps = protocol.HarnessSession{Model: session.Model, Permissions: session.Permissions, ID: session.ID, TerminalID: string(tid), SessionID: session.HolonID, HarnessType: protocol.HarnessType(session.AgentType), Prompt: session.Prompt, ResumeTarget: session.ResumeTarget, RolloutPath: session.RolloutPath, InputState: session.InputState, Activity: session.Activity}
	return driver, ps, runtimeDir, nil
}

// launch receives a constructed command so start, resume, and fork share terminal
// ownership and observation without inferring the operation from conversation IDs.
func (s *Service) launch(ctx context.Context, session Session, driver harness.Driver, ps protocol.HarnessSession, runtimeDir string, cmd *exec.Cmd, dimensions terminals.Dimensions) error {
	launched := false
	defer func() {
		if !launched {
			if stopper, ok := driver.(interface {
				Stop(context.Context, string) error
			}); ok {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = stopper.Stop(cleanup, ps.TerminalID)
			}
		}
	}()
	holonID, agentID := session.HolonID, session.ID
	tid := terminals.TerminalID(ps.TerminalID)
	monitor := driver.Monitor
	var prepared harness.MonitorRuntime
	if preparer, ok := driver.(harness.PreparedMonitorDriver); ok {
		var err error
		prepared, err = preparer.PrepareMonitor(runtimeDir)
		if err != nil {
			return err
		}
		if prepared != nil {
			monitor = prepared.Monitor
			defer func() {
				if !launched {
					_ = prepared.Close()
				}
			}()
		}
	}
	if err := s.state.Bind(ctx, holonID, agentID, string(tid)); err != nil {
		return fmt.Errorf("bind agent terminal: %w", err)
	}
	launchedAt := time.Now()
	pid, err := s.launcher.Launch(ctx, terminals.LaunchSpec{TerminalID: tid, Kind: terminals.LaunchCommand, Command: cmd.Path, Arguments: cmd.Args[1:], Environment: cmd.Env, CWD: cmd.Dir, Dimensions: dimensions})
	if err != nil {
		return fmt.Errorf("launch agent terminal: %w", err)
	}
	if err = s.state.Running(ctx, holonID, agentID); err != nil {
		_ = s.launcher.Signal(ctx, tid)
		return fmt.Errorf("mark agent running: %w", err)
	}
	monitorContext, stop := context.WithCancel(s.ctx)
	var actions chan protocol.TerminalAttentionAction
	if ps.HarnessType == protocol.HarnessClaudeCode {
		actions = make(chan protocol.TerminalAttentionAction, 16)
	}
	s.mu.Lock()
	if old, ok := s.monitors[agentID]; ok {
		old.cancel()
	}
	s.monitors[agentID] = executionMonitor{terminal: ps.TerminalID, driver: driver, cancel: stop, actions: actions}
	s.mu.Unlock()
	launched = true
	go func() {
		if prepared != nil {
			defer prepared.Close()
		}
		monitor(monitorContext, harness.MonitorSpec{ProcessID: pid, LaunchedAt: launchedAt, Worktree: session.Worktree, HarnessSession: ps, RuntimeDir: runtimeDir, AttentionActions: actions}, func(event harness.Event) {
			if monitorContext.Err() != nil {
				if event.Acknowledge != nil {
					event.Acknowledge(monitorContext.Err())
				}
				return
			}
			event.TerminalID = ps.TerminalID
			if event.Type == harness.EventRuntimeFailed {
				current, err := s.state.Session(monitorContext, holonID, agentID)
				if err == nil && current.TerminalID == ps.TerminalID {
					_ = s.state.Observed(monitorContext, holonID, agentID, event)
					_ = s.state.Failed(monitorContext, holonID, agentID, event.ObservabilityMessage)
					_ = s.launcher.Signal(monitorContext, tid)
				}
				return
			}
			err := s.state.Observed(monitorContext, holonID, agentID, event)
			if event.Acknowledge != nil {
				event.Acknowledge(err)
			}
		})
	}()
	return nil
}

func (s *Service) Cancel(ctx context.Context, holonID, agentID string) error {
	session, err := s.state.Session(ctx, holonID, agentID)
	if err != nil {
		return err
	}
	if session.ID == "" {
		return errors.New("agent session not found")
	}
	// The adapter resolves the durable binding, keeping process identity outside this domain.
	if session.TerminalID == "" {
		return nil
	}
	stopErr := s.StopTerminal(ctx, session.TerminalID)
	return errors.Join(stopErr, s.launcher.Signal(ctx, terminals.TerminalID(session.TerminalID)))
}
func (s *Service) Submit(ctx context.Context, holonID, agentID, prompt string) error {
	session, err := s.state.Session(ctx, holonID, agentID)
	if err != nil {
		return err
	}
	if session.TerminalID == "" {
		return errors.New("agent terminal is unavailable")
	}
	return s.launcher.Input(ctx, terminals.TerminalID(session.TerminalID), harness.SubmitPrompt(protocol.HarnessType(session.AgentType), prompt))
}

// ObserveTerminalInput resolves attention only for explicit cancellation keys
// successfully delivered to this launch. Escape sequences (such as arrows),
// pasted text, and Enter within multi-step dialogs are not prompt dismissals.
func (s *Service) ObserveTerminalInput(id terminals.TerminalID, data []byte) {
	var action protocol.TerminalAttentionAction
	switch string(data) {
	case "\x1b":
		action = protocol.TerminalAttentionCancel
	case "\x03":
		action = protocol.TerminalAttentionInterrupt
	default:
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, monitor := range s.monitors {
		if monitor.terminal == string(id) && monitor.actions != nil {
			select {
			case monitor.actions <- action:
			default:
			}
			return
		}
	}
}

func (s *Service) Close() { s.cancel() }

// StopExecution releases server-owned work before the terminal coordinator
// tears down the native PTY. Browser detach does not call this boundary.
func (s *Service) StopExecution(ctx context.Context, holonID, agentID string) error {
	session, err := s.state.Session(ctx, holonID, agentID)
	if err != nil {
		return err
	}
	return s.StopTerminal(ctx, session.TerminalID)
}

// StopTerminal releases the launch matching a process completion, including a
// native /quit. An old terminal cannot cancel a replacement launch's monitor.
func (s *Service) StopTerminal(ctx context.Context, terminalID string) error {
	s.mu.Lock()
	var monitor executionMonitor
	for agent, candidate := range s.monitors {
		if candidate.terminal == terminalID {
			monitor = candidate
			delete(s.monitors, agent)
			break
		}
	}
	s.mu.Unlock()
	if monitor.cancel == nil {
		return nil
	}
	defer monitor.cancel()
	stopper, ok := monitor.driver.(interface {
		Stop(context.Context, string) error
	})
	if !ok {
		return nil
	}
	return stopper.Stop(ctx, terminalID)
}
