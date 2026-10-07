package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalenv"
)

// Models reads the model catalogue from the SDK initialization handshake. It
// sends no user message, so discovery does not start an inference turn.
func Models(ctx context.Context, repositoryPath string) ([]protocol.ModelChoice, error) {
	return ModelsWithPrefix(ctx, repositoryPath, commandprefix.New("claude"))
}

func ModelsWithPrefix(ctx context.Context, repositoryPath string, prefix commandprefix.Prefix) ([]protocol.ModelChoice, error) {
	cmd := prefix.CommandContext(ctx, "--print", "--input-format", "stream-json",
		"--output-format", "stream-json", "--verbose", "--no-session-persistence",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--settings", `{"disableAllHooks":true}`)
	cmd.Dir = repositoryPath
	// Preserve provider/model settings and authentication, but do not launch
	// repository hooks or MCP servers just to populate a Settings dropdown.
	for _, value := range terminalenv.HarnessEnvironment(os.Environ(), "", "", repositoryPath) {
		if !strings.HasPrefix(value, "CLAUDECODE=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	defer stdout.Close()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("Claude Code model discovery failed: %w", err)
	}
	defer func() {
		_ = cmd.Cancel()
		_ = cmd.Wait()
	}()
	const requestID = "holark-model-discovery"
	if _, err := stdin.Write([]byte(`{"type":"control_request","request_id":"` + requestID + `","request":{"subtype":"initialize"}}` + "\n")); err != nil {
		return nil, fmt.Errorf("Claude Code model discovery initialization failed: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var message struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Subtype   string `json:"subtype"`
				Response  struct {
					Models []struct {
						Value         string `json:"value"`
						ResolvedModel string `json:"resolvedModel"`
						DisplayName   string `json:"displayName"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.Type != "control_response" || message.Response.RequestID != requestID {
			continue
		}
		if message.Response.Subtype != "success" {
			return nil, errors.New("Claude Code rejected model discovery initialization")
		}
		models := []protocol.ModelChoice{}
		for _, model := range message.Response.Response.Models {
			// Holark supplies its own Agent default entry. Pin resolved IDs when
			// available so a versioned label matches the selection at launch.
			if model.Value == "default" {
				continue
			}
			id := model.ResolvedModel
			if id == "" {
				id = model.Value
			}
			if id != "" && protocol.ValidModel(id) {
				models = append(models, protocol.ModelChoice{ID: id, Label: model.DisplayName})
			}
		}
		if len(models) == 0 {
			return nil, errors.New("Claude Code returned no models")
		}
		return models, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("Claude Code model discovery response failed: %w", err)
	}
	return nil, errors.New("Claude Code exited without a model catalogue")
}
