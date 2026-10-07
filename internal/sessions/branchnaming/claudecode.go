package branchnaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/protocol"
)

type ClaudeCodeNamer struct {
	commands   commandprefix.Resolver
	executable string
	timeout    time.Duration
	run        func(*exec.Cmd) error
}

type ClaudeCodeNamerOption func(*ClaudeCodeNamer)

func NewClaudeCodeNamer(options ...ClaudeCodeNamerOption) *ClaudeCodeNamer {
	namer := &ClaudeCodeNamer{executable: "claude", timeout: defaultCodexTimeout, run: runCommand}
	for _, option := range options {
		option(namer)
	}
	return namer
}

func WithClaudeExecutable(value string) ClaudeCodeNamerOption {
	return func(namer *ClaudeCodeNamer) { namer.executable = value }
}

func WithClaudeTimeout(value time.Duration) ClaudeCodeNamerOption {
	return func(namer *ClaudeCodeNamer) { namer.timeout = value }
}

func WithClaudeCommandRunner(value func(*exec.Cmd) error) ClaudeCodeNamerOption {
	return func(namer *ClaudeCodeNamer) { namer.run = value }
}

func (namer *ClaudeCodeNamer) Name(ctx context.Context, input Input) (Proposal, error) {
	if err := validateInput(input); err != nil || namer == nil || namer.executable == "" {
		return Proposal{}, errors.New("invalid branch naming input")
	}
	prefix, err := commandprefix.Resolve(ctx, namer.commands, protocol.HarnessClaudeCode, namer.executable)
	if err != nil {
		return Proposal{}, err
	}
	timeout := namer.timeout
	if timeout <= 0 {
		timeout = defaultCodexTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := prefix.CommandContext(ctx,
		"--print", "--output-format", "json", "--json-schema", outputSchema(input.GenerateTitle),
		"--tools", "", "--no-session-persistence", branchPrompt(input),
	)
	command.Env = os.Environ()
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	run := namer.run
	if run == nil {
		run = runCommand
	}
	if err := run(command); err != nil {
		if ctx.Err() != nil {
			return Proposal{}, ctx.Err()
		}
		return Proposal{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
	}
	var envelope struct {
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || len(envelope.StructuredOutput) == 0 {
		return Proposal{}, errors.New("Claude Code branch namer returned invalid structured output")
	}
	return parseProposal(envelope.StructuredOutput, input.GenerateTitle)
}

func validateInput(input Input) error {
	if strings.TrimSpace(input.Prompt) == "" || input.SessionID == "" || input.Project.ID == "" ||
		!utf8.ValidString(input.Title) || !utf8.ValidString(input.Prompt) ||
		len(input.PromptTemplate) > protocol.MaxPromptBytes || !utf8.ValidString(input.PromptTemplate) {
		return errors.New("invalid branch naming input")
	}
	return nil
}
