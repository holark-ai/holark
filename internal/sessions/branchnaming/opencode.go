package branchnaming

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/protocol"
)

type OpenCodeNamer struct {
	commands   commandprefix.Resolver
	executable string
	timeout    time.Duration
	run        func(*exec.Cmd) error
}

type OpenCodeNamerOption func(*OpenCodeNamer)

func NewOpenCodeNamer(options ...OpenCodeNamerOption) *OpenCodeNamer {
	namer := &OpenCodeNamer{executable: "opencode", timeout: defaultCodexTimeout, run: runCommand}
	for _, option := range options {
		option(namer)
	}
	return namer
}

func WithOpenCodeExecutable(value string) OpenCodeNamerOption {
	return func(namer *OpenCodeNamer) { namer.executable = value }
}
func WithOpenCodeTimeout(value time.Duration) OpenCodeNamerOption {
	return func(namer *OpenCodeNamer) { namer.timeout = value }
}
func WithOpenCodeCommandRunner(value func(*exec.Cmd) error) OpenCodeNamerOption {
	return func(namer *OpenCodeNamer) { namer.run = value }
}

func (namer *OpenCodeNamer) Name(ctx context.Context, input Input) (Proposal, error) {
	if err := validateInput(input); err != nil || namer == nil || namer.executable == "" {
		return Proposal{}, errors.New("invalid branch naming input")
	}
	prefix, err := commandprefix.Resolve(ctx, namer.commands, protocol.HarnessOpenCode, namer.executable)
	if err != nil {
		return Proposal{}, err
	}
	timeout := namer.timeout
	if timeout <= 0 {
		timeout = defaultCodexTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runtimeDir, err := os.MkdirTemp("", "holark-opencode-namer-*")
	if err != nil {
		return Proposal{}, err
	}
	defer os.RemoveAll(runtimeDir)
	configPath := filepath.Join(runtimeDir, "opencode.json")
	if err = os.WriteFile(configPath, []byte("{\"tools\":{\"*\":false}}\n"), 0o600); err != nil {
		return Proposal{}, err
	}
	command := prefix.CommandContext(ctx, "run", "--format", "json", branchPrompt(input))
	command.Env = append(os.Environ(), "OPENCODE_CONFIG="+configPath)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	run := namer.run
	if run == nil {
		run = runCommand
	}
	if err = run(command); err != nil {
		if ctx.Err() != nil {
			return Proposal{}, ctx.Err()
		}
		return Proposal{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
	}
	text, err := finalOpenCodeText(output.Bytes())
	if err != nil {
		return Proposal{}, err
	}
	return parseProposal(openCodeProposalPayload(text), input.GenerateTitle)
}

func openCodeProposalPayload(text string) []byte {
	trimmed := strings.TrimSpace(text)
	firstLineEnd := strings.IndexByte(trimmed, '\n')
	if firstLineEnd < 0 {
		return []byte(trimmed)
	}
	openingFence := strings.TrimSpace(trimmed[:firstLineEnd])
	if openingFence != "```" && !strings.EqualFold(openingFence, "```json") {
		return []byte(trimmed)
	}
	body := strings.TrimSpace(trimmed[firstLineEnd+1:])
	lastLineEnd := strings.LastIndexByte(body, '\n')
	if lastLineEnd < 0 || strings.TrimSpace(body[lastLineEnd+1:]) != "```" {
		return []byte(trimmed)
	}
	return []byte(strings.TrimSpace(body[:lastLineEnd]))
}

func finalOpenCodeText(raw []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	final := ""
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Part struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"part"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return "", errors.New("OpenCode branch namer returned an invalid JSON event")
		}
		if event.Type == "text" {
			if event.Part.Type == "text" && strings.TrimSpace(event.Part.Text) != "" {
				final = event.Part.Text
			} else if strings.TrimSpace(event.Text) != "" {
				final = event.Text
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(final) == "" {
		return "", errors.New("OpenCode branch namer returned no final text event")
	}
	return final, nil
}
