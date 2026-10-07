package harness

import (
	"context"
	"fmt"
	"time"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/harness/claudecode"
	"github.com/holark-ai/holark/internal/harness/opencode"
	"github.com/holark-ai/holark/internal/protocol"
)

// ModelLister is optional: a driver without discovery still supports its default.
type ModelLister interface {
	Models(context.Context, string) ([]protocol.ModelChoice, error)
}

func (registry *Registry) Models(ctx context.Context, kind protocol.HarnessType, repositoryPath string) ([]protocol.ModelChoice, error) {
	driver, ok := registry.Driver(kind)
	if !ok {
		return nil, ErrHarnessUnavailable
	}
	lister, ok := driver.(ModelLister)
	if !ok {
		return []protocol.ModelChoice{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	models, err := lister.Models(ctx, repositoryPath)
	if err != nil {
		return nil, err
	}
	result := make([]protocol.ModelChoice, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		if model.ID == "" || !protocol.ValidModel(model.ID) || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		if model.Label == "" {
			model.Label = model.ID
		}
		result = append(result, model)
	}
	return result, nil
}

func (driver codexDriver) Models(ctx context.Context, _ string) ([]protocol.ModelChoice, error) {
	prefix, err := commandprefix.Resolve(ctx, driver.commands, protocol.HarnessCodex, "codex")
	if err != nil {
		return nil, err
	}
	if lister, ok := driver.manager.(interface {
		ModelsWithPrefix(context.Context, commandprefix.Prefix) ([]protocol.ModelChoice, error)
	}); ok {
		return lister.ModelsWithPrefix(ctx, prefix)
	}
	lister, ok := driver.manager.(interface {
		Models(context.Context) ([]protocol.ModelChoice, error)
	})
	if !ok {
		return nil, fmt.Errorf("Codex model discovery is unavailable")
	}
	return lister.Models(ctx)
}

func (driver claudeDriver) Models(ctx context.Context, repositoryPath string) ([]protocol.ModelChoice, error) {
	executable := driver.executable
	if executable == "" {
		executable = "claude"
	}
	prefix, err := commandprefix.Resolve(ctx, driver.commands, protocol.HarnessClaudeCode, executable)
	if err != nil {
		return nil, err
	}
	return claudecode.ModelsWithPrefix(ctx, repositoryPath, prefix)
}

func (driver opencodeDriver) Models(ctx context.Context, repositoryPath string) ([]protocol.ModelChoice, error) {
	executable := driver.executable
	if executable == "" {
		executable = "opencode"
	}
	prefix, err := commandprefix.Resolve(ctx, driver.commands, protocol.HarnessOpenCode, executable)
	if err != nil {
		return nil, err
	}
	return opencode.ModelsWithPrefix(ctx, repositoryPath, prefix)
}
