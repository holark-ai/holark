// Package holonworkflow coordinates the explicit issue-to-Holon action.
package holonworkflow

import (
	"context"
	"strings"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/issues/comments"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/repository"
)

type Issues interface {
	Snapshot(context.Context, string) (issueworkflow.Snapshot, error)
}
type Repository interface {
	PrepareBranch(context.Context, string) (repository.Preparation, error)
	DefaultBranch() string
}
type Holons interface {
	Create(context.Context, holons.Create) (holons.Holon, error)
}
type Service struct {
	issues     Issues
	repository Repository
	holons     Holons
	templates  prompttemplates.Reader
}

func New(i Issues, r Repository, h Holons, readers ...prompttemplates.Reader) *Service {
	service := &Service{issues: i, repository: r, holons: h}
	if len(readers) > 0 {
		service.templates = readers[0]
	}
	return service
}
func (s *Service) Start(ctx context.Context, id string) (holons.Holon, error) {
	in := holons.Create{Kind: holons.KindIssue}
	if preflight, ok := s.holons.(agentPreflighter); ok {
		if err := preflight.PreflightSelection(ctx, &in); err != nil {
			return holons.Holon{}, err
		}
	}
	if in.AgentType == "" {
		in.AgentType = "codex"
	}
	var syncErr error
	synced := false
	if syncer, ok := s.issues.(interface {
		SyncComments(context.Context, string) (comments.Discussion, error)
	}); ok {
		_, syncErr = syncer.SyncComments(ctx, id)
		synced = syncErr == nil
	}
	snapshot, e := s.issues.Snapshot(ctx, id)
	if e != nil {
		return holons.Holon{}, e
	}
	definition, _ := prompttemplates.DefinitionByKey(prompttemplates.IssueAgentPlanKey)
	template := definition.DefaultValue
	if s.templates != nil {
		template = s.templates.Read(ctx, prompttemplates.IssueAgentPlanKey)
	}
	body := strings.TrimSpace(snapshot.Body)
	if body == "" {
		body = "No description provided."
	}
	discussion := snapshot.Discussion.Text
	if discussion == "" {
		discussion = comments.BuildContext(comments.Discussion{}).Text
	}
	if syncErr != nil {
		discussion = "Warning: GitHub comment sync failed. This discussion may be stale. Use holark issue comment sync " + snapshot.IssueID + " to refresh.\n" + discussion
	} else if synced {
		discussion = "Discussion synchronized with GitHub before agent startup.\n" + discussion
	}
	prompt := prompttemplates.Render(template, map[string]string{"issue_title": snapshot.Title, "issue_body": body, "issue_comments": discussion})
	if !prompttemplates.HasVariable(template, "issue_comments") {
		prompt += "\n\n" + discussion
	}
	prepared, e := s.repository.PrepareBranch(ctx, s.repository.DefaultBranch())
	if e != nil {
		return holons.Holon{}, e
	}
	in.Title, in.Prompt, in.IssueID = snapshot.Title, prompt, snapshot.IssueID
	in.BaseBranch, in.BaseCommit = prepared.Branch, prepared.Commit
	return s.holons.Create(ctx, in)
}

type agentPreflighter interface {
	PreflightSelection(context.Context, *holons.Create) error
}
