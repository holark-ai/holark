package opencode

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalenv"
)

func Models(ctx context.Context, repositoryPath string) ([]protocol.ModelChoice, error) {
	return ModelsWithPrefix(ctx, repositoryPath, commandprefix.New("opencode"))
}

func ModelsWithPrefix(ctx context.Context, repositoryPath string, prefix commandprefix.Prefix) ([]protocol.ModelChoice, error) {
	cmd := prefix.CommandContext(ctx, "models")
	cmd.Dir = repositoryPath
	cmd.Env = terminalenv.HarnessEnvironment(os.Environ(), "", "", repositoryPath)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("OpenCode model discovery failed: %w", err)
	}
	models := []protocol.ModelChoice{}
	for _, line := range strings.Split(string(output), "\n") {
		id := strings.TrimSpace(line)
		if strings.Contains(id, "/") && protocol.ValidModel(id) {
			models = append(models, protocol.ModelChoice{ID: id, Label: id})
		}
	}
	return models, nil
}
