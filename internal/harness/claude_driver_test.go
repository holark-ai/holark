package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/protocol"
)

const claudeDriverTestSessionID = "123e4567-e89b-42d3-a456-426614174000"

func TestDefaultRegistryIncludesSupportedHarnesses(t *testing.T) {
	registry := DefaultRegistry()
	if _, ok := registry.Driver(protocol.HarnessCodex); !ok {
		t.Fatal("Codex driver is missing")
	}
	if _, ok := registry.Driver(protocol.HarnessClaudeCode); !ok {
		t.Fatal("Claude Code driver is missing")
	}
	if _, ok := registry.Driver(protocol.HarnessOpenCode); !ok {
		t.Fatal("OpenCode driver is missing")
	}
	if _, ok := registry.Driver(protocol.HarnessType("unknown")); ok {
		t.Fatal("unknown harness driver was registered")
	}
}

func TestClaudeDriverResumeCommandUsesExistingTranscript(t *testing.T) {
	transcriptPath := filepath.Join(t.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(transcriptPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	arguments := claudeDriverResumeArgs(t, protocol.HarnessSession{
		ID:           "harness-session-1",
		HarnessType:  protocol.HarnessClaudeCode,
		ResumeTarget: claudeDriverTestSessionID,
		RolloutPath:  transcriptPath,
	}, "")
	if len(arguments) < 3 || arguments[1] != "--resume" || arguments[2] != claudeDriverTestSessionID {
		t.Fatalf("resume args = %#v, want existing Claude session", arguments)
	}
}

func TestClaudeDriverResumeCommandStartsFreshWhenTranscriptIsMissing(t *testing.T) {
	arguments := claudeDriverResumeArgs(t, protocol.HarnessSession{
		ID:           "harness-session-1",
		HarnessType:  protocol.HarnessClaudeCode,
		ResumeTarget: claudeDriverTestSessionID,
		RolloutPath:  filepath.Join(t.TempDir(), "missing.jsonl"),
		Prompt:       "original task",
	}, "new task")
	assertFreshClaudeCommand(t, arguments)
	if arguments[len(arguments)-1] != "new task" {
		t.Fatalf("fresh retry args = %#v, want follow-up prompt", arguments)
	}
}

func TestClaudeDriverResumeCommandStartsFreshWhenTranscriptPathIsEmpty(t *testing.T) {
	arguments := claudeDriverResumeArgs(t, protocol.HarnessSession{
		ID:           "harness-session-1",
		HarnessType:  protocol.HarnessClaudeCode,
		ResumeTarget: claudeDriverTestSessionID,
	}, "")
	assertFreshClaudeCommand(t, arguments)
}

func TestClaudeDriverResumeCommandAttemptsResumeAfterOtherTranscriptStatError(t *testing.T) {
	notDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notDirectory, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	arguments := claudeDriverResumeArgs(t, protocol.HarnessSession{
		ID:           "harness-session-1",
		HarnessType:  protocol.HarnessClaudeCode,
		ResumeTarget: claudeDriverTestSessionID,
		RolloutPath:  filepath.Join(notDirectory, "conversation.jsonl"),
	}, "")
	if len(arguments) < 3 || arguments[1] != "--resume" || arguments[2] != claudeDriverTestSessionID {
		t.Fatalf("resume args after non-ENOENT stat error = %#v, want existing Claude session", arguments)
	}
}

func TestClaudeDriverResumeCommandPreservesPromptOnFreshRetry(t *testing.T) {
	const prompt = "continue with the original task"
	arguments := claudeDriverResumeArgs(t, protocol.HarnessSession{
		ID:           "harness-session-1",
		HarnessType:  protocol.HarnessClaudeCode,
		ResumeTarget: claudeDriverTestSessionID,
		RolloutPath:  filepath.Join(t.TempDir(), "missing.jsonl"),
		Prompt:       prompt,
	}, "")
	assertFreshClaudeCommand(t, arguments)
	if len(arguments) < 2 || arguments[len(arguments)-2] != "--" || arguments[len(arguments)-1] != prompt {
		t.Fatalf("fresh retry args = %#v, want original prompt %q", arguments, prompt)
	}
}

func claudeDriverResumeArgs(t *testing.T, harnessSession protocol.HarnessSession, followUpPrompt string) []string {
	t.Helper()
	driver := claudeDriver{executable: "claude", hookExecutable: "holark-hook"}
	command, err := driver.ResumeCommand(ResumeSpec{
		RepositoryPath: t.TempDir(),
		SessionID:      "session-1",
		FollowUpPrompt: followUpPrompt,
		HarnessSession: harnessSession,
		RuntimeDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return command.Args
}

func assertFreshClaudeCommand(t *testing.T, arguments []string) {
	t.Helper()
	if len(arguments) < 3 || arguments[1] != "--session-id" || arguments[2] == "" {
		t.Fatalf("fresh args = %#v, want a new Claude session", arguments)
	}
	for _, argument := range arguments[1:] {
		if argument == "--resume" {
			t.Fatalf("fresh args = %#v, unexpectedly resumed Claude", arguments)
		}
	}
}
