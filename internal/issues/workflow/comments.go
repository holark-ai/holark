package workflow

import (
	"context"
	"fmt"

	"github.com/holark-ai/holark/internal/issues"
	"github.com/holark-ai/holark/internal/issues/comments"
)

type CommentTransport interface {
	ListComments(context.Context, issues.Issue, string) ([]comments.Comment, *bool, error)
	CreateComment(context.Context, issues.Issue, string, string) (comments.Comment, error)
	UpdateComment(context.Context, issues.Issue, string, comments.Comment, string) (comments.Comment, error)
	DeleteComment(context.Context, issues.Issue, string, comments.Comment) error
}

type CommentOutcome string

const (
	CommentAccepted  CommentOutcome = "accepted"
	CommentUncertain CommentOutcome = "uncertain"
)

// CommentMutationError also carries the available provider reference for recovery.
type CommentMutationError struct {
	Outcome  CommentOutcome
	GitHubID string
	URL      string
	Err      error
}

func (e *CommentMutationError) Error() string { return e.Err.Error() }
func (e *CommentMutationError) Unwrap() error { return e.Err }

type CommentRejectedError struct {
	Status  int
	Message string
}

func (e *CommentRejectedError) Error() string { return e.Message }

// WithComments is configured once by the composition root, before serving requests.
func (s *Service) WithComments(c comments.Store) *Service { s.comments = c; return s }
func (s *Service) ListComments(ctx context.Context, id string) (comments.Discussion, error) {
	if _, err := s.projection.Get(ctx, id); err != nil {
		return comments.Discussion{}, err
	}
	if s.comments == nil {
		return comments.Discussion{}, ErrIssueSourceUnavailable
	}
	return s.comments.ListComments(ctx, id)
}
func (s *Service) SyncComments(ctx context.Context, id string) (comments.Discussion, error) {
	unlock := s.lockIssue(id)
	defer unlock()
	issue, project, unlockProject, err := s.loadIssueForMutation(ctx, id)
	if err != nil {
		return comments.Discussion{}, err
	}
	defer unlockProject()
	transport, err := s.commentTransport(issue)
	if err != nil {
		return comments.Discussion{}, err
	}
	remote, can, err := transport.ListComments(ctx, issue, project.RepositoryURL)
	if err != nil {
		return comments.Discussion{}, err
	}
	if err = s.comments.ReconcileComments(ctx, id, remote, can, s.now().UTC()); err != nil {
		return comments.Discussion{}, err
	}
	return s.comments.ListComments(ctx, id)
}
func (s *Service) commentTransport(issue issues.Issue) (CommentTransport, error) {
	if issue.SyncProvider != "github" || issue.SyncExternalID == "" {
		return nil, ErrProviderIdentityRequired
	}
	t, ok := s.transport.(CommentTransport)
	if !ok || s.comments == nil {
		return nil, ErrIssueSourceUnavailable
	}
	return t, nil
}
func (s *Service) CreateComment(ctx context.Context, id, body string) (comments.Comment, error) {
	if err := comments.ValidateBody(body); err != nil {
		return comments.Comment{}, err
	}
	return s.mutateComment(ctx, id, "", body, "create")
}
func (s *Service) UpdateComment(ctx context.Context, id, body string) (comments.Comment, error) {
	if err := comments.ValidateBody(body); err != nil {
		return comments.Comment{}, err
	}
	return s.mutateComment(ctx, "", id, body, "update")
}
func (s *Service) DeleteComment(ctx context.Context, id string) error {
	_, err := s.mutateComment(ctx, "", id, "", "delete")
	return err
}
func (s *Service) mutateComment(ctx context.Context, issueID, id, body, operation string) (comments.Comment, error) {
	if s.comments == nil {
		return comments.Comment{}, ErrIssueSourceUnavailable
	}
	if id != "" {
		c, err := s.comments.GetComment(ctx, id)
		if err != nil {
			return comments.Comment{}, err
		}
		issueID = c.IssueID
	}
	unlock := s.lockIssue(issueID)
	defer unlock()
	issue, project, unlockProject, err := s.loadIssueForMutation(ctx, issueID)
	if err != nil {
		return comments.Comment{}, err
	}
	defer unlockProject()
	t, err := s.commentTransport(issue)
	if err != nil {
		return comments.Comment{}, err
	}
	var c comments.Comment
	if id != "" {
		c, err = s.comments.GetComment(ctx, id)
		if err != nil {
			return c, err
		}
	}
	switch operation {
	case "create":
		c, err = t.CreateComment(ctx, issue, project.RepositoryURL, body)
	case "update":
		c, err = t.UpdateComment(ctx, issue, project.RepositoryURL, c, body)
	case "delete":
		err = t.DeleteComment(ctx, issue, project.RepositoryURL, c)
	}
	if err != nil {
		return comments.Comment{}, err
	}
	c.IssueID = issueID
	var stored comments.Comment
	if operation == "delete" {
		err = s.comments.DeleteComment(ctx, id)
	} else {
		stored, err = s.comments.UpsertComment(ctx, c)
	}
	if err != nil {
		return comments.Comment{}, &CommentMutationError{Outcome: CommentAccepted, GitHubID: c.GitHubID, URL: c.URL, Err: fmt.Errorf("GitHub accepted the comment change; sync issue %s to recover: %w", issueID, err)}
	}
	return stored, nil
}
