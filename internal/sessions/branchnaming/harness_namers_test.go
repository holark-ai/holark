package branchnaming

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/protocol"
)

func namingInput() Input {
	return Input{SessionID: "holon-1", Project: protocol.Project{ID: "local"}, Prompt: "Fix the parser"}
}

func TestRunCommandSupportsPreconfiguredOutput(t *testing.T) {
	var output strings.Builder
	command := exec.Command("sh", "-c", "printf generated")
	command.Stdout, command.Stderr = &output, &output
	if err := runCommand(command); err != nil {
		t.Fatal(err)
	}
	if output.String() != "generated" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestHarnessNamersIncludeCapturedOutputInCommandErrors(t *testing.T) {
	commandErr := errors.New("command failed")
	runner := func(command *exec.Cmd) error {
		_, _ = io.WriteString(command.Stderr, "harness exploded")
		return commandErr
	}
	for _, test := range []struct {
		name  string
		namer Namer
	}{
		{name: "Claude Code", namer: NewClaudeCodeNamer(WithClaudeCommandRunner(runner))},
		{name: "OpenCode", namer: NewOpenCodeNamer(WithOpenCodeCommandRunner(runner))},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.namer.Name(t.Context(), namingInput())
			if !errors.Is(err, commandErr) || !strings.Contains(err.Error(), "harness exploded") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestClaudeCodeNamerUsesStructuredPrintModeWithoutToolsOrPersistence(t *testing.T) {
	namer := NewClaudeCodeNamer(WithClaudeCommandRunner(func(command *exec.Cmd) error {
		args := command.Args[1:]
		for _, required := range []string{"--print", "--output-format", "json", "--json-schema", "--tools", "", "--no-session-persistence"} {
			if !slices.Contains(args, required) {
				t.Fatalf("arguments %q do not contain %q", args, required)
			}
		}
		_, _ = io.WriteString(command.Stdout, `{"structured_output":{"slug":"fix-parser"}}`)
		return nil
	}))
	proposal, err := namer.Name(t.Context(), namingInput())
	if err != nil || proposal.Slug != "fix-parser" {
		t.Fatalf("proposal = %+v, %v", proposal, err)
	}
}

func TestClaudeCodeNamerRejectsInvalidStructuredOutput(t *testing.T) {
	namer := NewClaudeCodeNamer(WithClaudeCommandRunner(func(command *exec.Cmd) error {
		_, _ = io.WriteString(command.Stdout, `{"result":"{\\"slug\\":\\"silent-fallback\\"}"}`)
		return nil
	}))
	if _, err := namer.Name(t.Context(), namingInput()); err == nil {
		t.Fatal("expected invalid structured output error")
	}
}

func TestOpenCodeNamerUsesJSONRunWithTemporaryNoToolsConfiguration(t *testing.T) {
	namer := NewOpenCodeNamer(WithOpenCodeCommandRunner(func(command *exec.Cmd) error {
		if len(command.Args) < 4 || command.Args[1] != "run" || command.Args[2] != "--format" || command.Args[3] != "json" {
			t.Fatalf("arguments = %q", command.Args)
		}
		config := ""
		for _, value := range command.Env {
			if strings.HasPrefix(value, "OPENCODE_CONFIG=") {
				config = strings.TrimPrefix(value, "OPENCODE_CONFIG=")
			}
		}
		data, err := os.ReadFile(config)
		if err != nil || !strings.Contains(string(data), `"*":false`) {
			t.Fatalf("config = %q, %v", data, err)
		}
		_, _ = io.WriteString(command.Stdout, "{\"type\":\"step_start\"}\n{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"{\\\"slug\\\":\\\"fix-parser\\\"}\"}}\n")
		return nil
	}))
	proposal, err := namer.Name(t.Context(), namingInput())
	if err != nil || proposal.Slug != "fix-parser" {
		t.Fatalf("proposal = %+v, %v", proposal, err)
	}
}

func TestOpenCodeNamerAcceptsMarkdownFencedProposal(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
	}{
		{name: "JSON fence", text: "```json\n{\"slug\":\"fix-parser\"}\n```"},
		{name: "plain fence", text: "```\n{\"slug\":\"fix-parser\"}\n```"},
	} {
		t.Run(test.name, func(t *testing.T) {
			namer := NewOpenCodeNamer(WithOpenCodeCommandRunner(func(command *exec.Cmd) error {
				event, err := json.Marshal(map[string]any{"type": "text", "part": map[string]string{"type": "text", "text": test.text}})
				if err != nil {
					return err
				}
				_, err = command.Stdout.Write(append(event, '\n'))
				return err
			}))
			proposal, err := namer.Name(t.Context(), namingInput())
			if err != nil || proposal.Slug != "fix-parser" {
				t.Fatalf("proposal = %+v, %v", proposal, err)
			}
		})
	}
}

func TestOpenCodeNamerRejectsMalformedOrMissingFinalTextEvents(t *testing.T) {
	for _, output := range []string{"not-json\n", "{\"type\":\"step_finish\"}\n"} {
		namer := NewOpenCodeNamer(WithOpenCodeCommandRunner(func(command *exec.Cmd) error { _, _ = io.WriteString(command.Stdout, output); return nil }))
		if _, err := namer.Name(t.Context(), namingInput()); err == nil {
			t.Fatalf("output %q should fail", output)
		}
	}
}

type dispatcherNamer struct {
	called   int
	proposal Proposal
	err      error
}

func (n *dispatcherNamer) Name(context.Context, Input) (Proposal, error) {
	n.called++
	return n.proposal, n.err
}

func TestDispatcherNeverFallsBackToCodex(t *testing.T) {
	codex := &dispatcherNamer{proposal: Proposal{Slug: "codex-fallback"}}
	claude := &dispatcherNamer{err: errors.New("Claude failed")}
	dispatcher := NewDispatcherWithNamers(map[protocol.HarnessType]Namer{protocol.HarnessCodex: codex, protocol.HarnessClaudeCode: claude})
	if _, err := dispatcher.NameFor(t.Context(), protocol.HarnessClaudeCode, namingInput()); err == nil {
		t.Fatal("expected Claude error")
	}
	if codex.called != 0 || claude.called != 1 {
		t.Fatalf("calls: Codex=%d Claude=%d", codex.called, claude.called)
	}
	if _, err := dispatcher.NameFor(t.Context(), protocol.HarnessOpenCode, namingInput()); err == nil {
		t.Fatal("expected missing namer error")
	}
	if codex.called != 0 {
		t.Fatal("missing OpenCode namer fell back to Codex")
	}
}
