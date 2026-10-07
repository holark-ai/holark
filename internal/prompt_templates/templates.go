package prompttemplates

import (
	_ "embed"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/protocol"
)

const (
	IssueAgentPlanKey                  = "issue_agent_plan"
	PullRequestReviewKey               = "pull_request_review"
	PullRequestMetadataKey             = "pull_request_metadata"
	PullRequestWorkerKey               = "pull_request_worker"
	PullRequestRebaseKey               = "pull_request_rebase"
	PullRequestRebaseCommitKey         = "pull_request_rebase_commit_follow_up"
	HolonRebaseKey                     = "holon_rebase"
	PullRequestWorkerAutoCommitKey     = "pull_request_worker_auto_commit_follow_up"
	PullRequestWorkerAssistedCommitKey = "pull_request_worker_assisted_commit_follow_up"
	CommitFollowUpKey                  = "commit_follow_up"
	CommitAgentKey                     = "commit_agent"
	BranchNamingKey                    = "branch_naming"
)

type Variable struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Definition struct {
	Key          string
	Name         string
	Use          string
	Variables    []Variable
	DefaultValue string
}

type Template struct {
	Key          string     `json:"key"`
	Name         string     `json:"name"`
	Use          string     `json:"use"`
	Variables    []Variable `json:"variables"`
	Value        string     `json:"value"`
	DefaultValue string     `json:"default_value"`
	Overridden   bool       `json:"overridden"`
}

// Keep the embedded files byte-for-byte: trailing newlines affect default override detection.
var (
	//go:embed prompts/issue_agent_plan.txt
	issueAgentPlanDefault string

	//go:embed prompts/pull_request_review.txt
	pullRequestReviewDefault string

	//go:embed prompts/generate_pr_description.txt
	pullRequestMetadataDefault string

	//go:embed prompts/pull_request_worker.txt
	pullRequestWorkerDefault string

	//go:embed prompts/holon_rebase.txt
	holonRebaseDefault string

	//go:embed prompts/pull_request_rebase.txt
	pullRequestRebaseDefault string

	//go:embed prompts/pull_request_rebase_commit_follow_up.txt
	pullRequestRebaseCommitDefault string

	//go:embed prompts/pull_request_worker_auto_commit_follow_up.txt
	pullRequestWorkerAutoCommitDefault string

	//go:embed prompts/pull_request_worker_assisted_commit_follow_up.txt
	pullRequestWorkerAssistedCommitDefault string

	//go:embed prompts/commit_agent.txt
	commitAgentDefault string

	//go:embed prompts/commit_follow_up.txt
	commitFollowUpDefault string

	//go:embed prompts/branch_naming.txt
	branchNamingDefault string
)

var definitions = []Definition{
	{
		Key:  IssueAgentPlanKey,
		Name: "Issue agent plan prompt",
		Use:  "Used when starting an agent from an issue. The agent should perform scope-proportionate repository inspection and wait in plan mode before editing.",
		Variables: []Variable{
			{Name: "issue_title", Description: "Issue title."},
			{Name: "issue_body", Description: "Issue body, or fallback text when empty."},
			{Name: "issue_comments", Description: "Recent issue discussion with freshness and truncation metadata."},
		},
		DefaultValue: issueAgentPlanDefault,
	},
	{
		Key:  PullRequestReviewKey,
		Name: "Pull request review prompt",
		Use:  "Used for assisted and automatic pull request review sessions. The agent writes a review artifact and summarizes the findings.",
		Variables: []Variable{
			{Name: "pull_request_title", Description: "Pull request title."},
			{Name: "pull_request_description", Description: "Pull request description."},
			{Name: "pull_request_summary", Description: "Formatted pull request description for existing customized prompts."},
			{Name: "base_commit", Description: "Commit used as the diff base."},
			{Name: "artifact_path", Description: "Path where the review JSON artifact must be written."},
		},
		DefaultValue: pullRequestReviewDefault,
	},
	{
		Key:  PullRequestMetadataKey,
		Name: "Pull request metadata prompt",
		Use:  "Used to generate or improve a pull request title and description from the repository diff.",
		Variables: []Variable{
			{Name: "base_commit", Description: "Commit used as the diff base."},
			{Name: "artifact_path", Description: "Path where the metadata JSON artifact must be written."},
			{Name: "max_title_characters", Description: "Maximum allowed pull request title length."},
			{Name: "max_description_characters", Description: "Maximum allowed pull request description length."},
		},
		DefaultValue: pullRequestMetadataDefault,
	},

	{
		Key:  PullRequestWorkerKey,
		Name: "Pull request comment worker prompt",
		Use:  "Used when starting a worker agent to address a selected pull request comment.",
		Variables: []Variable{
			{Name: "pull_request_title", Description: "Pull request title."},
			{Name: "comment_body", Description: "The full comment thread to address, including the original comment, its code location, and all replies available at worker startup."},
			{Name: "mode_instruction", Description: "Mode-specific completion and autonomy instruction for auto or assisted operation."},
			{Name: "artifact_path", Description: "Path where the reply JSON artifact must be written."},
		},
		DefaultValue: pullRequestWorkerDefault,
	},
	{
		Key:  HolonRebaseKey,
		Name: "Holon rebase prompt",
		Use:  "Used when forking a conversation to inspect and rebase the current workspace onto its publication target.",
		Variables: []Variable{
			{Name: "source_branch", Description: "Workspace branch being rebased."},
			{Name: "target_branch", Description: "Publication branch to rebase onto."},
			{Name: "target_commit", Description: "Exact fetched target commit for the rebase."},
		},
		DefaultValue: holonRebaseDefault,
	},
	{
		Key:  PullRequestRebaseKey,
		Name: "Pull request rebase prompt",
		Use:  "Used when starting a worker agent to resolve conflicts after a pull request rebase could not complete directly.",
		Variables: []Variable{
			{Name: "pull_request_title", Description: "Pull request title."},
			{Name: "base_branch", Description: "Target base branch for the rebase."},
			{Name: "source_commit", Description: "Exact prepared source commit in the current worktree."},
			{Name: "target_base_commit", Description: "Exact target commit for the rebase."},
			{Name: "head_branch", Description: "Pull request branch being rebased."},
			{Name: "artifact_path", Description: "Path where the empty rebase completion marker must be created."},
		},
		DefaultValue: pullRequestRebaseDefault,
	},
	{
		Key:  PullRequestRebaseCommitKey,
		Name: "PR rebase commit follow-up prompt",
		Use:  "Sent when a pull request rebase signals completion with uncommitted changes, or a rebase commit is requested.",
		Variables: []Variable{
			{Name: "artifact_path", Description: "Path of the empty rebase completion marker."},
		},
		DefaultValue: pullRequestRebaseCommitDefault,
	},
	{
		Key:  PullRequestWorkerAutoCommitKey,
		Name: "Auto PR worker commit follow-up prompt",
		Use:  "Sent to auto pull request comment workers when task completion is detected before the fix has been committed.",
		Variables: []Variable{
			{Name: "artifact_path", Description: "Path where the reply JSON artifact must exist."},
		},
		DefaultValue: pullRequestWorkerAutoCommitDefault,
	},
	{
		Key:  PullRequestWorkerAssistedCommitKey,
		Name: "Assisted PR worker commit follow-up prompt",
		Use:  "Sent to assisted pull request comment workers when task completion is detected before the fix has been committed.",
		Variables: []Variable{
			{Name: "artifact_path", Description: "Path where the reply JSON artifact must exist."},
		},
		DefaultValue: pullRequestWorkerAssistedCommitDefault,
	},
	{
		Key:          CommitAgentKey,
		Name:         "Commit agent prompt",
		Use:          "Sent to a forked commit agent to inspect changes, choose a commit message, and commit without another confirmation.",
		Variables:    []Variable{},
		DefaultValue: commitAgentDefault,
	},
	{
		Key:          CommitFollowUpKey,
		Name:         "Post-task commit follow-up prompt",
		Use:          "Sent to normal and issue agents when task completion is detected and the worktree has uncommitted changes.",
		Variables:    []Variable{},
		DefaultValue: commitFollowUpDefault,
	},
	{
		Key:  BranchNamingKey,
		Name: "Branch naming prompt",
		Use:  "Used by the node to ask Codex for a proposed branch name and session title before the session is published.",
		Variables: []Variable{
			{Name: "session_title", Description: "Session title."},
			{Name: "prompt", Description: "Original session prompt."},
		},
		DefaultValue: branchNamingDefault,
	},
}

var placeholderPattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

func Definitions() []Definition {
	result := append([]Definition(nil), definitions...)
	for index := range result {
		result[index].Variables = copyVariables(result[index].Variables)
	}
	return result
}

func DefinitionByKey(key string) (Definition, bool) {
	for _, definition := range definitions {
		if definition.Key == key {
			definition.Variables = copyVariables(definition.Variables)
			return definition, true
		}
	}
	return Definition{}, false
}

func Build(overrides map[string]string) []Template {
	result := make([]Template, 0, len(definitions))
	for _, definition := range definitions {
		value, overridden := overrides[definition.Key]
		if !overridden {
			value = definition.DefaultValue
		}
		result = append(result, Template{
			Key: definition.Key, Name: definition.Name, Use: definition.Use, Variables: copyVariables(definition.Variables),
			Value: value, DefaultValue: definition.DefaultValue, Overridden: overridden,
		})
	}
	return result
}

func Validate(definition Definition, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("template is empty")
	}
	if len(value) > protocol.MaxPromptBytes || !utf8.ValidString(value) {
		return errors.New("template is too large")
	}
	allowed := map[string]struct{}{}
	for _, variable := range definition.Variables {
		allowed[variable.Name] = struct{}{}
	}
	for _, raw := range rawPlaceholders(value) {
		match := placeholderPattern.FindStringSubmatch(raw)
		if match == nil || len(match) != 2 {
			return errors.New("template contains an invalid placeholder")
		}
		if _, ok := allowed[match[1]]; !ok {
			return errors.New("template contains an unsupported placeholder")
		}
	}
	return nil
}

func Render(template string, values map[string]string) string {
	return placeholderPattern.ReplaceAllStringFunc(template, func(match string) string {
		parts := placeholderPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		return values[parts[1]]
	})
}

func ValidateDefaults() error {
	for _, definition := range definitions {
		if err := Validate(definition, definition.DefaultValue); err != nil {
			return err
		}
	}
	return nil
}

func copyVariables(variables []Variable) []Variable {
	if len(variables) == 0 {
		return []Variable{}
	}
	return append([]Variable(nil), variables...)
}

func SupportedKeys() []string {
	keys := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		keys = append(keys, definition.Key)
	}
	sort.Strings(keys)
	return keys
}

func rawPlaceholders(value string) []string {
	var placeholders []string
	for start := strings.Index(value, "{{"); start >= 0; start = strings.Index(value, "{{") {
		end := strings.Index(value[start+2:], "}}")
		if end < 0 {
			placeholders = append(placeholders, value[start:])
			break
		}
		end = start + 2 + end + 2
		placeholders = append(placeholders, value[start:end])
		value = value[end:]
	}
	return placeholders
}

// HasVariable uses the same whitespace-tolerant syntax as Render.
func HasVariable(template, name string) bool {
	for _, match := range placeholderPattern.FindAllStringSubmatch(template, -1) {
		if match[1] == name {
			return true
		}
	}
	return false
}
