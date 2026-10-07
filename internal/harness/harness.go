package harness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/holark-ai/holark/internal/agentskills"
	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/harness/claudecode"
	"github.com/holark-ai/holark/internal/harness/cliprobe"
	"github.com/holark-ai/holark/internal/harness/codex"
	"github.com/holark-ai/holark/internal/protocol"
)

type StartSpec struct {
	RepositoryPath string
	SessionID      string
	Prompt         string
	HarnessSession protocol.HarnessSession
	RuntimeDir     string
}

type ResumeSpec struct {
	RequireExisting      bool
	RepositoryPath       string
	SessionID            string
	FollowUpPrompt       string
	HarnessSession       protocol.HarnessSession
	ResumeTargetResolved func(string, string)
	RuntimeDir           string
}

// ForkSpec keeps source conversation metadata separate from the destination
// session, whose own resume target is discovered after launch.
type ForkSpec struct {
	RepositoryPath     string
	SessionID          string
	Prompt             string
	SourceResumeTarget string
	SourceRolloutPath  string
	HarnessSession     protocol.HarnessSession
	RuntimeDir         string
}

type MonitorSpec struct {
	ProcessID        int
	LaunchedAt       time.Time
	Worktree         string
	HarnessSession   protocol.HarnessSession
	RuntimeDir       string
	AttentionActions <-chan protocol.TerminalAttentionAction
}

type EventType string

const (
	EventSessionDiscovered    EventType = "session_discovered"
	EventActivityChanged      EventType = "activity_changed"
	EventContextUsageChanged  EventType = "context_usage_changed"
	EventInputStateChanged    EventType = "input_state_changed"
	EventObservabilityChanged EventType = "observability_changed"
	EventRuntimeFailed        EventType = "runtime_failed"
)

type Event struct {
	Acknowledge          func(error)
	Type                 EventType
	TerminalID           string
	HarnessSessionID     string
	HarnessType          protocol.HarnessType
	ResumeTarget         string
	RolloutPath          string
	Activity             protocol.AgentActivity
	ContextTokens        *int64
	InputState           protocol.InputState
	ObservabilityStatus  protocol.ObservabilityStatus
	ObservabilityMessage string
}

// SubmitPrompt brackets the prompt as a paste so burst detection cannot consume
// the following Enter as a newline, then appends the harness-specific Enter key.
// Codex and OpenCode enable the Kitty keyboard protocol, so a raw carriage
// return is not a reliable Enter key once their composers are active.
func SubmitPrompt(harnessType protocol.HarnessType, prompt string) []byte {
	enter := "\r"
	if harnessType == protocol.HarnessCodex || harnessType == protocol.HarnessOpenCode {
		enter = "\x1b[13u"
	}
	return []byte("\x1b[200~" + prompt + "\x1b[201~" + enter)
}

type Driver interface {
	Probe(context.Context) protocol.HarnessCapability
	Command(StartSpec) (*exec.Cmd, error)
	ResumeCommand(ResumeSpec) (*exec.Cmd, error)
	ForkCommand(ForkSpec) (*exec.Cmd, error)
	Monitor(context.Context, MonitorSpec, func(Event))
}

// PreparedMonitorDriver binds observation resources before the native process
// starts. The launch owner closes them on failure or monitor cancellation.
type PreparedMonitorDriver interface {
	PrepareMonitor(runtimeDir string) (MonitorRuntime, error)
}

type MonitorRuntime interface {
	Monitor(context.Context, MonitorSpec, func(Event))
	Close() error
}

// PrivateRuntimeDriver is an optional driver capability for harnesses that
// need a private, per-launch directory shared by command construction and
// monitoring.
type PrivateRuntimeDriver interface {
	RequiresPrivateRuntime() bool
}

type SkillSource struct {
	FS   fs.FS
	Root string
}

type SkillInstallSpec struct {
	TargetDir string
	Sources   []SkillSource
}

type SkillInstaller interface {
	SkillInstall() (SkillInstallSpec, bool)
}

type Registry struct {
	drivers map[protocol.HarnessType]Driver
}

func NewRegistry(drivers map[protocol.HarnessType]Driver) *Registry {
	copy := make(map[protocol.HarnessType]Driver, len(drivers))
	for harnessType, driver := range drivers {
		if driver != nil {
			copy[harnessType] = driver
		}
	}
	return &Registry{drivers: copy}
}

func DefaultRegistry() *Registry {
	return RegistryWithCodex(codex.NewManager())
}

// ProbeOptions supplies discovery metadata and the current launch command resolver.
type ProbeOptions struct {
	Commands        commandprefix.Resolver
	Discovery       cliprobe.Discovery
	SupportPolicies map[protocol.HarnessType]cliprobe.SupportPolicy
}

func (options ProbeOptions) forHarness(kind protocol.HarnessType) cliprobe.Options {
	result := cliprobe.Options{Discovery: options.Discovery}
	if policy, ok := options.SupportPolicies[kind]; ok {
		result.SupportPolicy = &policy
	}
	return result
}

func RegistryWithCodex(manager CodexServer) *Registry {
	return RegistryWithProbeOptions(manager, ProbeOptions{})
}

func RegistryWithProbeOptions(manager CodexServer, options ProbeOptions) *Registry {
	return NewRegistry(map[protocol.HarnessType]Driver{
		protocol.HarnessCodex:      codexDriver{commands: options.Commands, manager: manager, probeOptions: options.forHarness(protocol.HarnessCodex)},
		protocol.HarnessClaudeCode: claudeDriver{commands: options.Commands, probeOptions: options.forHarness(protocol.HarnessClaudeCode)},
		protocol.HarnessOpenCode:   opencodeDriver{commands: options.Commands, probeOptions: options.forHarness(protocol.HarnessOpenCode)},
	})
}

func (registry *Registry) Driver(harnessType protocol.HarnessType) (Driver, bool) {
	if registry == nil {
		return nil, false
	}
	driver, ok := registry.drivers[harnessType]
	return driver, ok
}

func (registry *Registry) Probe(ctx context.Context) []protocol.HarnessCapability {
	if registry == nil || len(registry.drivers) == 0 {
		return nil
	}
	capabilities := make([]protocol.HarnessCapability, 0, len(registry.drivers))
	for harnessType, driver := range registry.drivers {
		capability := driver.Probe(ctx)
		if capability.Type == "" {
			capability.Type = harnessType
		}
		capabilities = append(capabilities, capability)
	}
	sort.Slice(capabilities, func(i, j int) bool {
		return capabilities[i].Type < capabilities[j].Type
	})
	return capabilities
}

var (
	ErrHarnessUnavailable = errors.New("harness driver is unavailable")
	ErrHarnessPreparation = errors.New("harness runtime preparation failed")
)

// CodexServer is the owned runtime port; command orchestration does not depend
// on a particular socket or process implementation.
type CodexServer interface {
	Prepare(context.Context, *exec.Cmd, protocol.HarnessSession) error
	Monitor(context.Context, string, func(codex.Observation))
	Stop(context.Context, string) error
}
type codexDriver struct {
	commands     commandprefix.Resolver
	manager      CodexServer
	probeOptions cliprobe.Options
}

func (driver codexDriver) Probe(ctx context.Context) protocol.HarnessCapability {
	prefix, err := commandprefix.Resolve(ctx, driver.commands, protocol.HarnessCodex, "codex")
	if err != nil {
		return commandFailure(protocol.HarnessCodex, err)
	}
	options := driver.probeOptions
	options.Prefix = &prefix
	return capabilityFromProbe(protocol.HarnessCodex, codex.ProbeWithOptions(ctx, prefix.Executable(), options))
}

func capabilityFromProbe(kind protocol.HarnessType, probe cliprobe.Result) protocol.HarnessCapability {
	return protocol.HarnessCapability{
		Type: kind, Available: probe.Available, AutomatedWorkflows: true,
		Version: probe.Version, UnavailableReason: probe.Reason,
		SupportStatus: probe.SupportStatus, SupportedRanges: probe.SupportedRanges,
		LatestSupportedVersion: probe.LatestSupportedVersion, Warning: probe.Warning,
	}
}

func (d codexDriver) Command(spec StartSpec) (*exec.Cmd, error) {
	prefix, err := commandprefix.Resolve(context.Background(), d.commands, protocol.HarnessCodex, "codex")
	if err != nil {
		return nil, err
	}
	cmd, err := codex.CommandWithPermissions(prefix, spec.RepositoryPath, spec.SessionID, spec.Prompt, spec.HarnessSession.Model, spec.HarnessSession.Permissions)
	return d.prepare(cmd, err, spec.HarnessSession, prefix)
}

func (codexDriver) SkillInstall() (SkillInstallSpec, bool) {
	return SkillInstallSpec{
		TargetDir: ".agents/skills/holark-integration",
		Sources: []SkillSource{
			{FS: agentskills.Content, Root: agentskills.HolarkIntegration},
			{FS: codex.SkillMetadata, Root: "agentskills"},
		},
	}, true
}

func (d codexDriver) prepare(cmd *exec.Cmd, err error, session protocol.HarnessSession, prefix commandprefix.Prefix) (*exec.Cmd, error) {
	if err != nil {
		return nil, err
	}
	if d.manager == nil {
		return nil, fmt.Errorf("%w: Codex app-server manager is required", ErrHarnessPreparation)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if runtime, ok := d.manager.(interface {
		PrepareWithPrefix(context.Context, *exec.Cmd, protocol.HarnessSession, commandprefix.Prefix) error
	}); ok {
		err = runtime.PrepareWithPrefix(ctx, cmd, session, prefix)
	} else {
		err = d.manager.Prepare(ctx, cmd, session)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHarnessPreparation, err)
	}
	return cmd, nil
}
func (d codexDriver) ResumeCommand(spec ResumeSpec) (*exec.Cmd, error) {
	if spec.HarnessSession.ResumeTarget == "" {
		prompt := spec.FollowUpPrompt
		if prompt == "" {
			prompt = spec.HarnessSession.Prompt
		}
		return d.Command(StartSpec{RepositoryPath: spec.RepositoryPath, SessionID: spec.SessionID, Prompt: prompt, HarnessSession: spec.HarnessSession})
	}
	prefix, err := commandprefix.Resolve(context.Background(), d.commands, protocol.HarnessCodex, "codex")
	if err != nil {
		return nil, err
	}
	cmd, err := codex.ResumeCommand(prefix.Executable(), spec.RepositoryPath, spec.SessionID, spec.HarnessSession.ResumeTarget, spec.FollowUpPrompt, prefix.Arguments()...)
	return d.prepare(cmd, err, spec.HarnessSession, prefix)
}
func (d codexDriver) Monitor(ctx context.Context, spec MonitorSpec, send func(Event)) {
	d.manager.Monitor(ctx, spec.HarnessSession.TerminalID, func(o codex.Observation) {
		e := Event{Acknowledge: o.Acknowledge, HarnessSessionID: spec.HarnessSession.ID, HarnessType: protocol.HarnessCodex, TerminalID: spec.HarnessSession.TerminalID, ResumeTarget: o.Identity, InputState: o.Input, Activity: o.Activity, ContextTokens: o.ContextTokens, ObservabilityStatus: o.Status, ObservabilityMessage: o.Message}
		switch {
		case o.Failed:
			e.Type = EventRuntimeFailed
		case o.Identity != "":
			e.Type = EventSessionDiscovered
		case o.ContextTokens != nil:
			e.Type = EventContextUsageChanged
		case o.Input != "":
			e.Type = EventInputStateChanged
		case o.Activity != "" && o.Status == "":
			e.Type = EventActivityChanged
		default:
			e.Type = EventObservabilityChanged
		}
		send(e)
	})
}
func (d codexDriver) Stop(ctx context.Context, terminalID string) error {
	return d.manager.Stop(ctx, terminalID)
}
func (d codexDriver) ForkCommand(spec ForkSpec) (*exec.Cmd, error) {
	prefix, err := commandprefix.Resolve(context.Background(), d.commands, protocol.HarnessCodex, "codex")
	if err != nil {
		return nil, err
	}
	cmd, err := codex.ForkCommand(prefix.Executable(), spec.RepositoryPath, spec.SessionID, spec.SourceResumeTarget, spec.Prompt, prefix.Arguments()...)
	return d.prepare(cmd, err, spec.HarnessSession, prefix)
}

type claudeDriver struct {
	commands       commandprefix.Resolver
	probeOptions   cliprobe.Options
	executable     string
	hookExecutable string
}

func (claudeDriver) RequiresPrivateRuntime() bool {
	return true
}

func (driver claudeDriver) Probe(ctx context.Context) protocol.HarnessCapability {
	executable := driver.executable
	if executable == "" {
		executable = "claude"
	}
	prefix, err := commandprefix.Resolve(ctx, driver.commands, protocol.HarnessClaudeCode, executable)
	if err != nil {
		return commandFailure(protocol.HarnessClaudeCode, err)
	}
	executable = prefix.Executable()
	options := driver.probeOptions
	options.Prefix = &prefix
	return capabilityFromProbe(protocol.HarnessClaudeCode, claudecode.ProbeWithOptions(ctx, executable, options))
}

func (driver claudeDriver) SkillInstall() (SkillInstallSpec, bool) {
	return SkillInstallSpec{
		TargetDir: ".claude/skills/holark-integration",
		Sources: []SkillSource{
			{FS: agentskills.Content, Root: agentskills.HolarkIntegration},
		},
	}, true
}

func (driver claudeDriver) Command(spec StartSpec) (*exec.Cmd, error) {
	hookExecutable, err := driver.resolvedHookExecutable()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHarnessPreparation, err)
	}
	executable := driver.executable
	if executable == "" {
		executable = "claude"
	}
	prefix, err := commandprefix.Resolve(context.Background(), driver.commands, protocol.HarnessClaudeCode, executable)
	if err != nil {
		return nil, err
	}
	command, err := claudecode.CommandWithPrefix(prefix, hookExecutable, spec.RuntimeDir, spec.RepositoryPath, spec.SessionID, spec.HarnessSession.ID, spec.Prompt, spec.HarnessSession.Model)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHarnessPreparation, err)
	}
	return applyClaudePermissions(command, prefix, spec.HarnessSession.Permissions)
}

func (driver claudeDriver) ResumeCommand(spec ResumeSpec) (*exec.Cmd, error) {
	rolloutPath := spec.HarnessSession.RolloutPath
	startFresh := spec.HarnessSession.ResumeTarget == "" || rolloutPath == ""
	if !startFresh {
		_, rolloutErr := os.Stat(rolloutPath)
		startFresh = errors.Is(rolloutErr, os.ErrNotExist)
	}
	if startFresh {
		if spec.RequireExisting {
			return nil, errors.New("Stored Claude Code transcript is unavailable; its identity has been retained")
		}
		prompt := spec.FollowUpPrompt
		if prompt == "" {
			prompt = spec.HarnessSession.Prompt
		}
		return driver.Command(StartSpec{
			RepositoryPath: spec.RepositoryPath,
			SessionID:      spec.SessionID,
			Prompt:         prompt,
			HarnessSession: spec.HarnessSession,
			RuntimeDir:     spec.RuntimeDir,
		})
	}
	hookExecutable, err := driver.resolvedHookExecutable()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHarnessPreparation, err)
	}
	executable := driver.executable
	if executable == "" {
		executable = "claude"
	}
	prefix, err := commandprefix.Resolve(context.Background(), driver.commands, protocol.HarnessClaudeCode, executable)
	if err != nil {
		return nil, err
	}
	executable = prefix.Executable()
	command, err := claudecode.ResumeCommand(executable, hookExecutable, spec.RuntimeDir, spec.RepositoryPath, spec.SessionID, spec.HarnessSession.ID, spec.HarnessSession.ResumeTarget, spec.FollowUpPrompt, prefix.Arguments()...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHarnessPreparation, err)
	}
	return applyClaudePermissions(command, prefix, spec.HarnessSession.Permissions)
}

func (driver claudeDriver) ForkCommand(spec ForkSpec) (*exec.Cmd, error) {
	if spec.SourceRolloutPath == "" {
		return nil, fmt.Errorf("%w: Claude Code source transcript path is missing", ErrHarnessPreparation)
	}
	info, err := os.Stat(spec.SourceRolloutPath)
	if err != nil {
		return nil, fmt.Errorf("%w: Claude Code source transcript: %v", ErrHarnessPreparation, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: Claude Code source transcript is not a regular file", ErrHarnessPreparation)
	}
	hookExecutable, err := driver.resolvedHookExecutable()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHarnessPreparation, err)
	}
	executable := driver.executable
	if executable == "" {
		executable = "claude"
	}
	prefix, err := commandprefix.Resolve(context.Background(), driver.commands, protocol.HarnessClaudeCode, executable)
	if err != nil {
		return nil, err
	}
	executable = prefix.Executable()
	command, err := claudecode.ForkCommand(executable, hookExecutable, spec.RuntimeDir, spec.RepositoryPath, spec.SessionID, spec.HarnessSession.ID, spec.SourceResumeTarget, spec.Prompt, prefix.Arguments()...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHarnessPreparation, err)
	}
	return applyClaudePermissions(command, prefix, spec.HarnessSession.Permissions)
}

func (driver claudeDriver) PrepareMonitor(runtimeDir string) (MonitorRuntime, error) {
	observer, err := claudecode.PrepareObserver(runtimeDir)
	if err != nil {
		return nil, err
	}
	return &claudeMonitor{observer: observer}, nil
}

type claudeMonitor struct{ observer *claudecode.Observer }

func (monitor *claudeMonitor) Close() error { return monitor.observer.Close() }
func (monitor *claudeMonitor) Monitor(ctx context.Context, spec MonitorSpec, send func(Event)) {
	monitorClaude(ctx, spec, monitor.observer, send)
}
func (driver claudeDriver) Monitor(ctx context.Context, spec MonitorSpec, send func(Event)) {
	monitorClaude(ctx, spec, nil, send)
}
func monitorClaude(ctx context.Context, spec MonitorSpec, observer *claudecode.Observer, send func(Event)) {
	claudecode.MonitorWithOptions(ctx, spec.RuntimeDir, spec.Worktree, spec.HarnessSession.ID, claudecode.MonitorOptions{
		Observer: observer, ProcessID: spec.ProcessID, LaunchedAt: spec.LaunchedAt,
		InitialInputState: spec.HarnessSession.InputState, AttentionActions: spec.AttentionActions,
	}, func(event claudecode.MonitorEvent) {
		if send == nil {
			return
		}
		switch event.Type {
		case claudecode.MonitorMetadata:
			send(Event{Type: EventSessionDiscovered, HarnessSessionID: spec.HarnessSession.ID, HarnessType: protocol.HarnessClaudeCode, ResumeTarget: event.ResumeTarget, RolloutPath: event.RolloutPath})
		case claudecode.MonitorActivity:
			send(Event{Type: EventActivityChanged, HarnessSessionID: spec.HarnessSession.ID, HarnessType: protocol.HarnessClaudeCode, Activity: event.Activity})
		case claudecode.MonitorInputState:
			send(Event{Type: EventInputStateChanged, HarnessSessionID: spec.HarnessSession.ID, HarnessType: protocol.HarnessClaudeCode, InputState: event.InputState, Activity: event.Activity})
		case claudecode.MonitorContextUsage:
			send(Event{Type: EventContextUsageChanged, HarnessSessionID: spec.HarnessSession.ID, HarnessType: protocol.HarnessClaudeCode, ContextTokens: event.ContextTokens})
		case claudecode.MonitorObservability:
			send(Event{Type: EventObservabilityChanged, HarnessSessionID: spec.HarnessSession.ID, HarnessType: protocol.HarnessClaudeCode, ObservabilityStatus: event.ObservabilityStatus, Activity: event.Activity, ObservabilityMessage: event.ObservabilityMessage})
		case claudecode.MonitorDiagnostic:
			log.Printf("Claude Code harness %s: %s", spec.HarnessSession.ID, event.ObservabilityMessage)
		}
	})
}

func (driver claudeDriver) resolvedHookExecutable() (string, error) {
	if driver.hookExecutable != "" {
		return driver.hookExecutable, nil
	}
	return os.Executable()
}

func commandFailure(kind protocol.HarnessType, err error) protocol.HarnessCapability {
	return protocol.HarnessCapability{Type: kind, UnavailableReason: err.Error()}
}
