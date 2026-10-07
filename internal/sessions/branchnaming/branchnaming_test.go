package branchnaming

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestComposeBranchKeepsReadableSlugAndValidatesProviderBranch(t *testing.T) {
	branch, err := ComposeBranch("fix-login-flow")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "holark/fix-login-flow" || !ValidBranch(branch) {
		t.Fatalf("branch = %q", branch)
	}

	invalidSlugs := []string{"", "ab", "Fix-Login", "fix_login", "fix--login", "fix-login-", "-fix-login", strings.Repeat("a", 49)}
	for _, slug := range invalidSlugs {
		if _, err := ComposeBranch(slug); !errors.Is(err, ErrInvalidSlug) {
			t.Fatalf("ComposeBranch(%q) error = %v, want invalid slug", slug, err)
		}
	}
	invalidBranches := []string{"fix-login-flow", "holark/fix_login", "holark/fix--login", "holark/Fix-Login"}
	for _, branch := range invalidBranches {
		if ValidBranch(branch) {
			t.Fatalf("ValidBranch(%q) = true", branch)
		}
	}
}

func TestCodexNamerBuildsStructuredReadOnlyEphemeralCommand(t *testing.T) {
	t.Setenv("CODEX_NAMER_MODEL", "gpt-5-mini")
	t.Setenv("CODEX_NAMER_EFFORT", "low")
	var gotArgs []string
	namer := NewCodexNamer(WithExecutable("/usr/local/bin/codex"), WithCommandRunner(func(command *exec.Cmd) error {
		gotArgs = append([]string(nil), command.Args...)
		outputPath := valueAfter(command.Args, "--output-last-message")
		if outputPath == "" {
			t.Fatal("missing output path")
		}
		schemaPath := valueAfter(command.Args, "--output-schema")
		rawSchema, err := os.ReadFile(schemaPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(rawSchema), `"additionalProperties": false`) || !strings.Contains(string(rawSchema), `"slug"`) || strings.Contains(string(rawSchema), `"title"`) {
			t.Fatalf("schema = %s", rawSchema)
		}
		return os.WriteFile(outputPath, []byte(`{"slug":"fix-login-flow"}`), 0o600)
	}))
	proposal, err := namer.Name(context.Background(), Input{SessionID: "session-one", Project: protocol.Project{ID: "project-one"}, Title: "Repair authentication", Prompt: "Fix login flow"})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Slug != "fix-login-flow" || proposal.Title != "" {
		t.Fatalf("proposal = %+v", proposal)
	}
	if len(gotArgs) == 0 || gotArgs[0] != "/usr/local/bin/codex" || !containsArgument(gotArgs, "exec") || valueAfter(gotArgs, "--sandbox") != "read-only" || !containsArgument(gotArgs, "--ephemeral") || !containsArgument(gotArgs, "--skip-git-repo-check") {
		t.Fatalf("args = %#v", gotArgs)
	}
	if valueAfter(gotArgs, "--model") != "gpt-5-mini" || valueAfter(gotArgs, "--config") != `model_reasoning_effort="low"` {
		t.Fatalf("args missing model/effort: %#v", gotArgs)
	}
	prompt := gotArgs[len(gotArgs)-1]
	for _, want := range []string{"Repair authentication", "Fix login flow", "2–4 meaningful words", "lowercase ASCII kebab-case"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt = %q, want it to contain %q", prompt, want)
		}
	}
	for _, unwanted := range []string{"Holark", "holark/", "provider branch"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt = %q, do not want it to contain %q", prompt, unwanted)
		}
	}
	for _, want := range []string{"preserve the existing session title", "only the required `slug` field", "Do not return or suggest a title"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt = %q, want it to contain %q", prompt, want)
		}
	}
}

func TestCodexNamerSupportsStrictSlugOnlyOverridesForExplicitTitles(t *testing.T) {
	var gotPrompt string
	namer := NewCodexNamer(WithCommandRunner(func(command *exec.Cmd) error {
		gotPrompt = command.Args[len(command.Args)-1]
		rawSchema, err := os.ReadFile(valueAfter(command.Args, "--output-schema"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(rawSchema), `"required": ["slug"]`) {
			t.Fatalf("explicit-title schema = %s", rawSchema)
		}
		return os.WriteFile(valueAfter(command.Args, "--output-last-message"), []byte(`{"slug":"fix-login-flow"}`), 0o600)
	}))
	proposal, err := namer.Name(t.Context(), Input{SessionID: "session-one", Project: protocol.Project{ID: "project-one"}, Title: "Repair login", Prompt: "Fix login", PromptTemplate: "Custom override: {{prompt}}"})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Slug != "fix-login-flow" || proposal.Title != "" {
		t.Fatalf("slug-only proposal = %+v", proposal)
	}
	for _, want := range []string{"Custom override: Fix login", "preserve the existing session title", "only the required `slug` field"} {
		if !strings.Contains(gotPrompt, want) {
			t.Fatalf("custom prompt = %q, want it to contain %q", gotPrompt, want)
		}
	}

	if _, err := parseProposal([]byte(`{"slug":"fix-login-flow","title":"Unexpected"}`), false); err == nil {
		t.Fatal("slug-only mode accepted an undeclared title")
	}
}

func TestCodexNamerRequiresTitleWhenGeneratingTitleWithLegacyOverride(t *testing.T) {
	const legacyOverride = "Return a branch name for: {{prompt}}"
	namer := NewCodexNamer(WithCommandRunner(func(command *exec.Cmd) error {
		rawSchema, err := os.ReadFile(valueAfter(command.Args, "--output-schema"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(rawSchema), `"required": ["slug", "title"]`) {
			t.Fatalf("auto-title schema = %s", rawSchema)
		}
		prompt := command.Args[len(command.Args)-1]
		for _, want := range []string{"Return a branch name for: Fix login", "generate both identity fields", "only `slug` and `title`", "both fields are required"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("auto-title prompt = %q, want it to contain %q", prompt, want)
			}
		}
		return os.WriteFile(valueAfter(command.Args, "--output-last-message"), []byte(`{"slug":"fix-login-flow","title":"Repair login flow"}`), 0o600)
	}))
	proposal, err := namer.Name(t.Context(), Input{SessionID: "session-one", Project: protocol.Project{ID: "project-one"}, Prompt: "Fix login", PromptTemplate: legacyOverride, GenerateTitle: true})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Slug != "fix-login-flow" || proposal.Title != "Repair login flow" {
		t.Fatalf("auto-title proposal = %+v", proposal)
	}

	slugOnly := NewCodexNamer(WithCommandRunner(func(command *exec.Cmd) error {
		return os.WriteFile(valueAfter(command.Args, "--output-last-message"), []byte(`{"slug":"fix-login-flow"}`), 0o600)
	}))
	if _, err := slugOnly.Name(t.Context(), Input{SessionID: "session-one", Project: protocol.Project{ID: "project-one"}, Prompt: "Fix login", PromptTemplate: legacyOverride, GenerateTitle: true}); err == nil {
		t.Fatal("auto-title naming accepted a slug-only proposal")
	}
}

func TestCodexNamerRejectsInvalidResultsAndCommandFailures(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		run     func(*exec.Cmd) error
		timeout time.Duration
	}{
		{name: "invalid json", output: `{`},
		{name: "unknown field", output: `{"slug":"fix-login-flow","extra":true}`},
		{name: "invalid slug", output: `{"slug":"Fix_Login"}`},
		{name: "multiple JSON values", output: `{"slug":"fix-login-flow"} {"slug":"other-branch"}`},
		{name: "command failure", run: func(*exec.Cmd) error { return errors.New("command failed") }},
		{
			name:    "timeout",
			timeout: 10 * time.Millisecond,
			run: func(*exec.Cmd) error {
				time.Sleep(20 * time.Millisecond)
				return errors.New("command timed out")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := test.run
			if runner == nil {
				runner = func(command *exec.Cmd) error {
					return os.WriteFile(valueAfter(command.Args, "--output-last-message"), []byte(test.output), 0o600)
				}
			}
			options := []CodexNamerOption{WithCommandRunner(runner)}
			if test.timeout > 0 {
				options = append(options, WithTimeout(test.timeout))
			}
			namer := NewCodexNamer(options...)
			if _, err := namer.Name(context.Background(), Input{SessionID: "session-one", Project: protocol.Project{ID: "project-one"}, Prompt: "Do work"}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func containsArgument(arguments []string, want string) bool {
	for _, argument := range arguments {
		if argument == want {
			return true
		}
	}
	return false
}

func valueAfter(args []string, key string) string {
	for index := 0; index < len(args)-1; index++ {
		if args[index] == key {
			return args[index+1]
		}
	}
	return ""
}
