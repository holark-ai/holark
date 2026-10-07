package agentsettings

import (
	"context"
	"fmt"
	"strings"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/protocol"
)

type Commands map[protocol.HarnessType]string

type CommandStore interface {
	LoadCommands(context.Context) (Commands, error)
	// SaveCommands atomically merges supplied fields, preserving omitted harnesses.
	SaveCommands(context.Context, Commands) error
}

type LaunchCommands struct {
	store      CommandStore
	invalidate func(commandprefix.Prefix)
}

func NewLaunchCommands(store CommandStore, invalidate func(commandprefix.Prefix)) *LaunchCommands {
	return &LaunchCommands{store: store, invalidate: invalidate}
}

func defaultCommand(kind protocol.HarnessType) string {
	switch kind {
	case protocol.HarnessCodex:
		return "codex"
	case protocol.HarnessClaudeCode:
		return "claude"
	case protocol.HarnessOpenCode:
		return "opencode"
	}
	return ""
}

func (s *LaunchCommands) Current(ctx context.Context) (Commands, error) {
	values, err := s.store.LoadCommands(ctx)
	if err != nil {
		return nil, fmt.Errorf("load agent launch commands: %w", err)
	}
	result := Commands{protocol.HarnessCodex: "", protocol.HarnessClaudeCode: "", protocol.HarnessOpenCode: ""}
	for kind, value := range values {
		if !validHarness(kind) {
			return nil, fmt.Errorf("unknown saved harness %q", kind)
		}
		result[kind] = value
	}
	return result, nil
}

func parseCommand(kind protocol.HarnessType, value string) (commandprefix.Prefix, error) {
	if !validHarness(kind) {
		return commandprefix.Prefix{}, ErrInvalid
	}
	if strings.TrimSpace(value) == "" {
		value = defaultCommand(kind)
	}
	prefix, err := commandprefix.Parse(value)
	if err != nil {
		return commandprefix.Prefix{}, fmt.Errorf("%w: %s: %v", ErrInvalid, kind, err)
	}
	return prefix, nil
}

func (s *LaunchCommands) ResolveCommand(ctx context.Context, kind protocol.HarnessType) (commandprefix.Prefix, error) {
	values, err := s.Current(ctx)
	if err != nil {
		return commandprefix.Prefix{}, err
	}
	return parseCommand(kind, values[kind])
}

func (s *LaunchCommands) Save(ctx context.Context, values Commands) error {
	if len(values) == 0 {
		return fmt.Errorf("%w: provide at least one launch command", ErrInvalid)
	}
	normalized := make(Commands, 3)
	prefixes := make(map[protocol.HarnessType]commandprefix.Prefix, 3)
	for kind, value := range values {
		if len(value) > 4096 {
			return fmt.Errorf("%w: command is too long", ErrInvalid)
		}
		prefix, err := parseCommand(kind, value)
		if err != nil {
			return err
		}
		normalized[kind] = value
		if strings.TrimSpace(value) == "" {
			normalized[kind] = ""
		}
		prefixes[kind] = prefix
	}
	previous, err := s.Current(ctx)
	if err != nil {
		return err
	}
	if err := s.store.SaveCommands(ctx, normalized); err != nil {
		return fmt.Errorf("save agent launch commands: %w", err)
	}
	if s.invalidate != nil {
		for kind, prefix := range prefixes {
			if old, err := parseCommand(kind, previous[kind]); err == nil {
				s.invalidate(old)
			}
			s.invalidate(prefix)
		}
	}
	return nil
}

func validHarness(value protocol.HarnessType) bool {
	return value == protocol.HarnessCodex || value == protocol.HarnessClaudeCode || value == protocol.HarnessOpenCode
}
