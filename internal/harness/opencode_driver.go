package harness

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/holark-ai/holark/internal/agentskills"
	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/harness/cliprobe"
	"github.com/holark-ai/holark/internal/harness/opencode"
	"github.com/holark-ai/holark/internal/protocol"
)

type opencodeDriver struct {
	commands     commandprefix.Resolver
	probeOptions cliprobe.Options
	executable   string
}

func (opencodeDriver) RequiresPrivateRuntime() bool {
	return true
}

func (driver opencodeDriver) Probe(ctx context.Context) protocol.HarnessCapability {
	executable := driver.executable
	if executable == "" {
		executable = "opencode"
	}
	prefix, err := commandprefix.Resolve(ctx, driver.commands, protocol.HarnessOpenCode, executable)
	if err != nil {
		return commandFailure(protocol.HarnessOpenCode, err)
	}
	executable = prefix.Executable()
	options := driver.probeOptions
	options.Prefix = &prefix
	return capabilityFromProbe(protocol.HarnessOpenCode, opencode.ProbeWithOptions(ctx, executable, options))
}

func (opencodeDriver) SkillInstall() (SkillInstallSpec, bool) {
	return SkillInstallSpec{
		TargetDir: ".agents/skills/holark-integration",
		Sources: []SkillSource{
			{FS: agentskills.Content, Root: agentskills.HolarkIntegration},
		},
	}, true
}

func (driver opencodeDriver) Command(spec StartSpec) (*exec.Cmd, error) {
	executable := driver.executable
	if executable == "" {
		executable = "opencode"
	}
	prefix, err := commandprefix.Resolve(context.Background(), driver.commands, protocol.HarnessOpenCode, executable)
	if err != nil {
		return nil, err
	}
	command, err := opencode.CommandWithPrefixAndVersion(
		prefix, opencodeInstalledVersion(prefix), spec.RuntimeDir, spec.RepositoryPath, spec.SessionID,
		spec.HarnessSession.ID, spec.Prompt, spec.HarnessSession.Model,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHarnessPreparation, err)
	}
	return applyOpenCodePermissions(command, spec.HarnessSession.Permissions)
}

func (driver opencodeDriver) ResumeCommand(spec ResumeSpec) (*exec.Cmd, error) {
	if spec.HarnessSession.ResumeTarget == "" {
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
	executable := driver.executable
	if executable == "" {
		executable = "opencode"
	}
	prefix, err := commandprefix.Resolve(context.Background(), driver.commands, protocol.HarnessOpenCode, executable)
	if err != nil {
		return nil, err
	}
	executable = prefix.Executable()
	command, err := opencode.ResumeCommandWithVersion(
		executable, opencodeInstalledVersion(prefix), spec.RuntimeDir, spec.RepositoryPath, spec.SessionID,
		spec.HarnessSession.ID, spec.HarnessSession.ResumeTarget, spec.FollowUpPrompt,
		prefix.Arguments()...,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHarnessPreparation, err)
	}
	if spec.RequireExisting {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := opencode.VerifyResumeTarget(ctx, command, spec.HarnessSession.ResumeTarget, prefix); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrHarnessPreparation, err)
		}
	}
	return applyOpenCodePermissions(command, spec.HarnessSession.Permissions)
}

func (driver opencodeDriver) ForkCommand(spec ForkSpec) (*exec.Cmd, error) {
	executable := driver.executable
	if executable == "" {
		executable = "opencode"
	}
	prefix, err := commandprefix.Resolve(context.Background(), driver.commands, protocol.HarnessOpenCode, executable)
	if err != nil {
		return nil, err
	}
	executable = prefix.Executable()
	command, err := opencode.ForkCommandWithVersion(
		executable, opencodeInstalledVersion(prefix), spec.RuntimeDir, spec.RepositoryPath, spec.SessionID,
		spec.HarnessSession.ID, spec.SourceResumeTarget, spec.Prompt,
		prefix.Arguments()...,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHarnessPreparation, err)
	}
	return applyOpenCodePermissions(command, spec.HarnessSession.Permissions)
}

// opencodeInstalledVersion best-effort resolves the installed CLI version for
// per-operation v2 dispatch. Any failure returns "" so launch keeps the
// validated v1 behavior; version checks never gate execution.
func opencodeInstalledVersion(prefix commandprefix.Prefix) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := cliprobe.VersionPrefix(ctx, prefix)
	if err != nil {
		return ""
	}
	return output
}

func (opencodeDriver) Monitor(ctx context.Context, spec MonitorSpec, send func(Event)) {
	opencode.Monitor(ctx, spec.RuntimeDir, spec.HarnessSession.ResumeTarget, spec.HarnessSession.InputState, func(event opencode.MonitorEvent) {
		if send == nil {
			return
		}
		switch event.Type {
		case opencode.MonitorMetadata:
			send(Event{
				Type: EventSessionDiscovered, HarnessSessionID: spec.HarnessSession.ID,
				HarnessType: protocol.HarnessOpenCode, ResumeTarget: event.ResumeTarget,
			})
		case opencode.MonitorActivity:
			send(Event{Type: EventActivityChanged, HarnessSessionID: spec.HarnessSession.ID, HarnessType: protocol.HarnessOpenCode, Activity: event.Activity})
		case opencode.MonitorInputState:
			send(Event{
				Type: EventInputStateChanged, HarnessSessionID: spec.HarnessSession.ID,
				HarnessType: protocol.HarnessOpenCode, InputState: event.InputState, Activity: event.Activity,
			})
		case opencode.MonitorContextUsage:
			send(Event{
				Type: EventContextUsageChanged, HarnessSessionID: spec.HarnessSession.ID,
				HarnessType: protocol.HarnessOpenCode, ContextTokens: event.ContextTokens,
			})
		case opencode.MonitorObservability:
			send(Event{
				Type: EventObservabilityChanged, HarnessSessionID: spec.HarnessSession.ID,
				HarnessType: protocol.HarnessOpenCode, ObservabilityStatus: event.ObservabilityStatus, Activity: event.Activity,
				ObservabilityMessage: event.ObservabilityMessage,
			})
		}
	})
}
