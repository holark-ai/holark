package branchnaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/sessions"
)

const defaultCodexTimeout = 90 * time.Second

type CodexNamer struct {
	commands   commandprefix.Resolver
	executable string
	timeout    time.Duration
	run        func(*exec.Cmd) error
}

type CodexNamerOption func(*CodexNamer)

func NewCodexNamer(options ...CodexNamerOption) *CodexNamer {
	namer := &CodexNamer{
		executable: "codex",
		timeout:    defaultCodexTimeout,
		run:        runCommand,
	}
	for _, option := range options {
		option(namer)
	}
	return namer
}

func WithExecutable(executable string) CodexNamerOption {
	return func(namer *CodexNamer) {
		namer.executable = executable
	}
}

func WithTimeout(timeout time.Duration) CodexNamerOption {
	return func(namer *CodexNamer) {
		namer.timeout = timeout
	}
}

func WithCommandRunner(run func(*exec.Cmd) error) CodexNamerOption {
	return func(namer *CodexNamer) {
		namer.run = run
	}
}

func (namer *CodexNamer) Name(ctx context.Context, input Input) (Proposal, error) {
	if namer == nil || namer.executable == "" || strings.TrimSpace(input.Prompt) == "" ||
		input.SessionID == "" || input.Project.ID == "" || !utf8.ValidString(input.Title) || !utf8.ValidString(input.Prompt) ||
		len(input.PromptTemplate) > protocol.MaxPromptBytes || !utf8.ValidString(input.PromptTemplate) {
		return Proposal{}, errors.New("invalid branch naming input")
	}
	prefix, err := commandprefix.Resolve(ctx, namer.commands, protocol.HarnessCodex, namer.executable)
	if err != nil {
		return Proposal{}, err
	}
	timeout := namer.timeout
	if timeout <= 0 {
		timeout = defaultCodexTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tempDir, err := os.MkdirTemp("", "holark-branch-namer-*")
	if err != nil {
		return Proposal{}, err
	}
	defer os.RemoveAll(tempDir)
	schemaPath := filepath.Join(tempDir, "schema.json")
	outputPath := filepath.Join(tempDir, "output.json")
	if err := os.WriteFile(schemaPath, []byte(outputSchema(input.GenerateTitle)), 0o600); err != nil {
		return Proposal{}, err
	}

	command := prefix.CommandContext(ctx, codexArguments(input, schemaPath, outputPath)...)
	command.Env = os.Environ()
	run := namer.run
	if run == nil {
		run = runCommand
	}
	if err := run(command); err != nil {
		if ctx.Err() != nil {
			return Proposal{}, ctx.Err()
		}
		return Proposal{}, err
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return Proposal{}, err
	}
	proposal, err := parseProposal(raw, input.GenerateTitle)
	if err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

func codexArguments(input Input, schemaPath, outputPath string) []string {
	arguments := []string{
		"exec",
		"--sandbox", "read-only",
		"--ephemeral",
		"--skip-git-repo-check",
		"--output-schema", schemaPath,
		"--output-last-message", outputPath,
	}
	if model := strings.TrimSpace(os.Getenv("CODEX_NAMER_MODEL")); model != "" {
		arguments = append(arguments, "--model", model)
	}
	if effort := strings.TrimSpace(os.Getenv("CODEX_NAMER_EFFORT")); effort != "" {
		arguments = append(arguments, "--config", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}
	return append(arguments, branchPrompt(input))
}

func branchPrompt(input Input) string {
	template := input.PromptTemplate
	if template == "" {
		definition, _ := prompttemplates.DefinitionByKey(prompttemplates.BranchNamingKey)
		template = definition.DefaultValue
	}
	prompt := prompttemplates.Render(template, map[string]string{
		"session_title": input.Title,
		"prompt":        input.Prompt,
	})
	if input.GenerateTitle {
		return strings.TrimSpace(prompt) + "\n\nNaming mode: generate both identity fields. Ignore any conflicting response instructions above. Return exactly one JSON object containing only `slug` and `title`; both fields are required. The title must concisely describe the task in at most 80 Unicode characters."
	}
	return strings.TrimSpace(prompt) + "\n\nNaming mode: preserve the existing session title. Ignore any conflicting response instructions above. Generate only the branch slug and return exactly one JSON object containing only the required `slug` field. Do not return or suggest a title."
}

func parseProposal(raw []byte, requireTitle bool) (Proposal, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(string(raw))))
	decoder.DisallowUnknownFields()
	output := Proposal{}
	if requireTitle {
		if err := decoder.Decode(&output); err != nil {
			return Proposal{}, err
		}
	} else {
		var slugOnly struct {
			Slug string `json:"slug"`
		}
		if err := decoder.Decode(&slugOnly); err != nil {
			return Proposal{}, err
		}
		output.Slug = slugOnly.Slug
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Proposal{}, errors.New("branch namer returned multiple JSON values")
	}
	if !ValidSlug(output.Slug) {
		return Proposal{}, ErrInvalidSlug
	}
	output.Title = strings.TrimSpace(output.Title)
	if requireTitle && (output.Title == "" || !utf8.ValidString(output.Title) || !sessions.TitleWithinLimit(output.Title)) {
		return Proposal{}, errors.New("branch namer returned invalid title")
	}
	return Proposal{Slug: output.Slug, Title: output.Title}, nil
}

func outputSchema(requireTitle bool) string {
	if !requireTitle {
		return slugOnlySchema
	}
	return generatedIdentitySchema
}

func runCommand(command *exec.Cmd) error {
	if command.Stdout != nil || command.Stderr != nil {
		return command.Run()
	}
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, message)
}

const slugOnlySchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "slug": {
      "type": "string",
      "pattern": "^[a-z0-9][a-z0-9-]{1,46}[a-z0-9]$"
    }
  },
  "required": ["slug"]
}
`

const generatedIdentitySchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "slug": {
      "type": "string",
      "pattern": "^[a-z0-9][a-z0-9-]{1,46}[a-z0-9]$"
    },
    "title": {
      "type": "string",
      "minLength": 1,
      "maxLength": 80
    }
  },
  "required": ["slug", "title"]
}
`
