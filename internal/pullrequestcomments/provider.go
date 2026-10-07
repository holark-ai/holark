package pullrequestcomments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

type ProviderTarget struct {
	Provider          string
	RepositoryURL     string
	PullRequestNumber int
	PullRequestID     string
	HeadCommit        string
}

type RemoteComment struct {
	ProviderIdentity   ProviderIdentity
	ParentExternalID   string
	Body               string
	PublishedBody      string
	IdempotencyKey     string
	Scope              Scope
	Path               string
	OldPath            string
	Side               string
	Line               *int
	DiffHunk           string
	OriginalHeadCommit string
	Resolved           bool
	Author             ProviderAuthor
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type RemoteMutation struct {
	CommentID          string
	ParentCommentID    string
	ProviderIdentity   ProviderIdentity
	ParentExternalID   string
	Body               string
	ReplyContextBody   string `json:"-"`
	IdempotencyKey     string `json:"-"`
	Scope              Scope
	Path               string
	OldPath            string
	Side               string
	Line               *int
	DiffHunk           string
	OriginalHeadCommit string
}

type publicationPayload struct {
	RemoteMutation
	ReplyContextExternalID string `json:"quote_external_id,omitempty"`
}

type ProviderGateway interface {
	ListComments(context.Context, ProviderTarget) ([]RemoteComment, error)
	CreateComment(context.Context, ProviderTarget, RemoteMutation) (ProviderIdentity, error)
	UpdateComment(context.Context, ProviderTarget, RemoteMutation) error
	DeleteComment(context.Context, ProviderTarget, RemoteMutation) error
	ResolveThread(context.Context, ProviderTarget, RemoteMutation) error
	ReopenThread(context.Context, ProviderTarget, RemoteMutation) error
}

type ProviderError struct {
	err         error
	retryable   bool
	rateLimited bool
	retryAfter  time.Duration
}

func (providerError *ProviderError) Error() string             { return providerError.err.Error() }
func (providerError *ProviderError) RetryAfter() time.Duration { return providerError.retryAfter }
func (providerError *ProviderError) Unwrap() error             { return providerError.err }

func RetryableProviderError(err error, retryAfter time.Duration) error {
	return &ProviderError{err: err, retryable: true, retryAfter: retryAfter}
}

func RateLimitedProviderError(err error, retryAfter time.Duration) error {
	return &ProviderError{err: err, retryable: true, rateLimited: true, retryAfter: retryAfter}
}
func (providerError *ProviderError) RateLimited() bool { return providerError.rateLimited }

func PermanentProviderError(err error) error {
	return &ProviderError{err: err}
}

type ServiceOption func(*serviceOptions)

type serviceOptions struct {
	gateway          ProviderGateway
	authors          AuthorObserver
	workerReferences WorkerReferenceReader
	scheduler        RefreshScheduler
	clock            func() time.Time
}

func WithProviderGateway(gateway ProviderGateway, authors AuthorObserver) ServiceOption {
	return func(options *serviceOptions) { options.gateway, options.authors = gateway, authors }
}

func WithWorkerReferenceReader(reader WorkerReferenceReader) ServiceOption {
	return func(options *serviceOptions) { options.workerReferences = reader }
}

type PublicationJob struct {
	ID             int64
	CommentID      string
	PullRequestID  string
	Operation      string
	Payload        string
	Attempts       int
	NextAttemptAt  time.Time
	PermanentError string
	CreatedAt      time.Time
}

type publicationStore interface {
	SaveWithPublication(context.Context, Comment, bool, *PublicationJob) error
	DuePublicationPullRequests(context.Context, time.Time) ([]string, error)
	UpdatePublicationPayload(context.Context, int64, string) error
	EnqueuePublication(context.Context, PublicationJob) error
	DuePublications(context.Context, string, time.Time) ([]PublicationJob, error)
	PendingPublications(context.Context, string) ([]PublicationJob, error)
	CompletePublication(context.Context, int64) error
	FailPublication(context.Context, int64, int, time.Time, string) error
	CancelCommentPublications(context.Context, string) error
	SetProviderIdentity(context.Context, string, ProviderIdentity, PublicationState) error
}

type providerIntegration struct {
	localLocks   *keyedLocks
	report       func(error)
	comments     Repository
	publications publicationStore
	gateway      ProviderGateway
	authors      AuthorObserver
	now          func() time.Time
}

func newProviderIntegration(comments Repository, gateway ProviderGateway, authors AuthorObserver, now func() time.Time) *providerIntegration {
	publications, ok := comments.(publicationStore)
	if gateway == nil || !ok {
		return nil
	}
	return &providerIntegration{comments: comments, publications: publications, gateway: gateway, authors: authors, now: now}
}

func providerTarget(target PullRequestTarget) (ProviderTarget, bool) {
	if target.SyncProvider == "" || target.RepositoryURL == "" || target.ProviderPullRequest <= 0 {
		return ProviderTarget{}, false
	}
	return ProviderTarget{Provider: target.SyncProvider, RepositoryURL: target.RepositoryURL, PullRequestNumber: target.ProviderPullRequest, PullRequestID: target.ID, HeadCommit: target.HeadCommit}, true
}

// mutationJob determines publication eligibility using the same rules for users and workers.
func (integration *providerIntegration) mutationJob(ctx context.Context, target PullRequestTarget, operation string, comment Comment) (*PublicationJob, error) {
	if comment.PublicationState == PublicationDraft {
		return nil, nil
	}
	if _, ok := providerTarget(target); !ok {
		return nil, nil
	}
	if operation != "create" {
		if comment.ProviderIdentity.ExternalID == "" {
			if comment.PublicationState != PublicationPending {
				return nil, nil
			}
			// A queued creation reads the latest saved body when it runs.
			if operation == "update" {
				operation = "create"
			} else if !((operation == "resolve" || operation == "reopen") && comment.ParentCommentID == "" && comment.Scope != ScopePullRequest) {
				return nil, nil
			}
		} else if (operation == "resolve" || operation == "reopen") && comment.ProviderIdentity.Kind != "inline" {
			return nil, nil
		}
	}
	mutation := mutationFromComment(comment)
	quote := ""
	// New creations select quotation context only when their turn to publish arrives.
	if operation != "create" {
		quote = integration.attachParentAndReplyContext(ctx, comment, &mutation)
	}
	payload, err := json.Marshal(publicationPayload{RemoteMutation: mutation, ReplyContextExternalID: quote})
	if err != nil {
		return nil, err
	}
	now := integration.now().UTC()
	return &PublicationJob{CommentID: comment.ID, PullRequestID: comment.PullRequestID, Operation: operation, Payload: string(payload), NextAttemptAt: now, CreatedAt: now}, nil
}

func (integration *providerIntegration) cancelCommentPublications(ctx context.Context, commentID string) error {
	return integration.publications.CancelCommentPublications(ctx, commentID)
}

func (integration *providerIntegration) attachParentAndReplyContext(ctx context.Context, comment Comment, mutation *RemoteMutation) string {
	if comment.ParentCommentID == "" {
		return ""
	}
	parent, err := integration.comments.Get(ctx, comment.ParentCommentID)
	if err == nil {
		mutation.ParentExternalID = parent.ProviderIdentity.ExternalID
	}
	if err != nil || comment.Scope != ScopePullRequest {
		return ""
	}
	return integration.latestPublishedReplyBefore(ctx, parent, comment)
}

func (integration *providerIntegration) latestPublishedReplyBefore(ctx context.Context, root, comment Comment) string {
	selected := root
	comments, err := integration.comments.ListByPullRequest(ctx, comment.PullRequestID)
	if err != nil {
		integration.report(err)
		return root.ProviderIdentity.ExternalID
	}
	for _, candidate := range comments {
		if candidate.ID == comment.ID || candidate.ParentCommentID != root.ID ||
			candidate.Scope != comment.Scope ||
			candidate.ProviderIdentity.ExternalID == "" || candidate.PublicationState != PublicationPublished ||
			!commentPrecedes(candidate, comment) {
			continue
		}
		if selected.ID == root.ID || commentPrecedes(selected, candidate) {
			selected = candidate
		}
	}
	return selected.ProviderIdentity.ExternalID
}

func commentPrecedes(left, right Comment) bool {
	if left.CreatedAt.Equal(right.CreatedAt) {
		return left.ID < right.ID
	}
	return left.CreatedAt.Before(right.CreatedAt)
}

func (integration *providerIntegration) flush(ctx context.Context, target ProviderTarget) {
	jobs, err := integration.publications.DuePublications(ctx, target.PullRequestID, integration.now().UTC())
	if err != nil {
		integration.report(err)
		return
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		integration.process(ctx, target, job)
	}
}

func (integration *providerIntegration) pendingCommentIDs(ctx context.Context, pullRequestID string) (map[string]bool, error) {
	jobs, err := integration.publications.PendingPublications(ctx, pullRequestID)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		if job.CommentID != "" {
			result[job.CommentID] = true
		}
	}
	return result, nil
}

func (integration *providerIntegration) process(ctx context.Context, target ProviderTarget, job PublicationJob) {
	unlock := integration.localLocks.lock(target.PullRequestID)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()

	var payload publicationPayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		integration.report(integration.publications.FailPublication(ctx, job.ID, job.Attempts+1, job.NextAttemptAt, err.Error()))
		return
	}
	mutation := payload.RemoteMutation
	replyContextExternalID := payload.ReplyContextExternalID
	var comment, parent Comment
	if job.Operation == "create" || job.Operation == "update" || job.Operation == "resolve" || job.Operation == "reopen" {
		var getErr error
		comment, getErr = integration.comments.Get(ctx, job.CommentID)
		if errors.Is(getErr, ErrCommentNotFound) {
			integration.report(integration.publications.CompletePublication(ctx, job.ID))
			return
		}
		if getErr != nil {
			integration.report(getErr)
			return
		}
		mutation = mutationFromComment(comment)
		if comment.ParentCommentID != "" {
			parent, getErr = integration.comments.Get(ctx, comment.ParentCommentID)
			if getErr != nil && !errors.Is(getErr, ErrCommentNotFound) {
				integration.report(getErr)
				return
			}
			mutation.ParentExternalID = parent.ProviderIdentity.ExternalID
		}
		if (job.Operation == "resolve" || job.Operation == "reopen") && mutation.ProviderIdentity.ExternalID == "" {
			integration.report(integration.publications.FailPublication(ctx, job.ID, job.Attempts, integration.now().UTC().Add(time.Minute), ""))
			return
		}
		if job.Operation == "create" {
			if comment.ParentCommentID != "" && mutation.ParentExternalID == "" {
				if parent.ID != "" && parent.PublicationState == PublicationPending {
					integration.report(integration.publications.FailPublication(ctx, job.ID, job.Attempts, integration.now().UTC().Add(time.Minute), ""))
					return
				}
				integration.report(integration.publications.SetProviderIdentity(ctx, job.CommentID, ProviderIdentity{}, PublicationLocal))
				integration.report(integration.publications.CompletePublication(ctx, job.ID))
				return
			}
		}
	}
	if job.Operation == "create" && isOverviewReply(mutation) && replyContextExternalID == "" {
		replyContextExternalID = integration.latestPublishedReplyBefore(ctx, parent, comment)
		payload.ReplyContextExternalID = replyContextExternalID
		encoded, err := json.Marshal(payload)
		if err != nil {
			integration.report(err)
			return
		}
		if err := integration.publications.UpdatePublicationPayload(ctx, job.ID, string(encoded)); err != nil {
			integration.report(err)
			return
		}
	}
	unlock()
	locked = false
	var remote []RemoteComment
	if job.Operation == "create" || (job.Operation == "update" && isOverviewReply(mutation)) {
		var listErr error
		remote, listErr = integration.listCommentsForPublication(ctx, target)
		if listErr != nil {
			unlock = integration.localLocks.lock(target.PullRequestID)
			locked = true
			integration.failPublication(ctx, job, mutation, listErr)
			return
		}
	}
	if job.Operation == "create" || job.Operation == "update" {
		if isOverviewReply(mutation) {
			mutation.ReplyContextBody = publicationReplyContextBody(remote, replyContextExternalID, mutation.ParentExternalID, parent.Body)
		}
		if job.Operation == "create" {
			mutation.IdempotencyKey = comment.ID
		}
	}
	var err error
	switch job.Operation {
	case "create":
		var identity ProviderIdentity
		identity, err = integration.recoverOrCreate(ctx, target, mutation, remote)
		if err == nil {
			unlock = integration.localLocks.lock(target.PullRequestID)
			locked = true
			if saveErr := integration.publications.SetProviderIdentity(ctx, job.CommentID, identity, PublicationPublished); saveErr != nil {
				integration.report(saveErr)
				return // Preserve uncertain success for identity recovery on the next pass.
			}
		}
	case "update":
		err = integration.gateway.UpdateComment(ctx, target, mutation)
	case "delete":
		err = integration.gateway.DeleteComment(ctx, target, mutation)
	case "resolve":
		mutation, err = integration.ensureThreadIdentity(ctx, target, job.CommentID, mutation)
		if err != nil {
			break
		}
		err = integration.gateway.ResolveThread(ctx, target, mutation)
	case "reopen":
		mutation, err = integration.ensureThreadIdentity(ctx, target, job.CommentID, mutation)
		if err != nil {
			break
		}
		err = integration.gateway.ReopenThread(ctx, target, mutation)
	default:
		err = PermanentProviderError(fmt.Errorf("unsupported comment publication %q", job.Operation))
	}
	if !locked {
		unlock = integration.localLocks.lock(target.PullRequestID)
		locked = true
	}
	if err == nil && job.Operation != "create" && job.Operation != "delete" {
		if saveErr := integration.publications.SetProviderIdentity(ctx, job.CommentID, mutation.ProviderIdentity, PublicationPublished); saveErr != nil {
			integration.report(saveErr)
			return
		}
	}
	if err == nil {
		integration.report(integration.publications.CompletePublication(ctx, job.ID))
		return
	}
	integration.failPublication(ctx, job, mutation, err)
}

func (integration *providerIntegration) failPublication(ctx context.Context, job PublicationJob, mutation RemoteMutation, err error) {
	if ctx.Err() != nil {
		return
	}

	retryable, retryAfter := classifyProviderError(err)
	if !retryable {
		if job.CommentID != "" && job.Operation != "delete" {
			integration.report(integration.publications.SetProviderIdentity(ctx, job.CommentID, mutation.ProviderIdentity, PublicationFailed))
		}
		integration.report(integration.publications.FailPublication(ctx, job.ID, job.Attempts+1, job.NextAttemptAt, err.Error()))
		return
	}
	if retryAfter <= 0 {
		retryAfter = publicationBackoff(job.Attempts + 1)
	}
	integration.report(integration.publications.FailPublication(ctx, job.ID, job.Attempts+1, integration.now().UTC().Add(retryAfter), ""))
}

func (integration *providerIntegration) listCommentsForPublication(ctx context.Context, target ProviderTarget) ([]RemoteComment, error) {
	remote, err := integration.gateway.ListComments(ctx, target)
	if err != nil {
		_, retryAfter := classifyProviderError(err)
		return nil, RetryableProviderError(fmt.Errorf("list provider comments: %w", err), retryAfter)
	}
	return remote, nil
}

func isOverviewReply(mutation RemoteMutation) bool {
	return mutation.ParentCommentID != "" && mutation.Scope == ScopePullRequest
}

func publicationReplyContextBody(remote []RemoteComment, selectedExternalID, rootExternalID, storedRootBody string) string {
	if body, ok := remotePublishedBody(remote, selectedExternalID); ok {
		return body
	}
	if selectedExternalID != rootExternalID {
		if body, ok := remotePublishedBody(remote, rootExternalID); ok {
			return body
		}
	}
	return strings.TrimSpace(storedRootBody)
}

func remotePublishedBody(remote []RemoteComment, externalID string) (string, bool) {
	if externalID == "" {
		return "", false
	}
	for _, comment := range remote {
		if comment.ProviderIdentity.ExternalID != externalID {
			continue
		}
		if comment.PublishedBody != "" || comment.Body == "" {
			return comment.PublishedBody, true
		}
		return comment.Body, true
	}
	return "", false
}

func (integration *providerIntegration) ensureThreadIdentity(ctx context.Context, target ProviderTarget, commentID string, mutation RemoteMutation) (RemoteMutation, error) {
	if mutation.ProviderIdentity.Kind != "inline" || mutation.ProviderIdentity.ThreadID != "" {
		return mutation, nil
	}
	remote, err := integration.gateway.ListComments(ctx, target)
	if err != nil {
		return mutation, err
	}
	for _, comment := range remote {
		if comment.ProviderIdentity.ExternalID != mutation.ProviderIdentity.ExternalID {
			continue
		}
		mutation.ProviderIdentity = comment.ProviderIdentity
		unlock := integration.localLocks.lock(target.PullRequestID)
		err := integration.publications.SetProviderIdentity(ctx, commentID, mutation.ProviderIdentity, PublicationPending)
		unlock()
		if err != nil {
			integration.report(err)
			return mutation, RetryableProviderError(err, 0)
		}
		return mutation, nil
	}
	return mutation, PermanentProviderError(errors.New("provider review thread identity is unavailable"))
}

func (integration *providerIntegration) recoverOrCreate(ctx context.Context, target ProviderTarget, mutation RemoteMutation, remote []RemoteComment) (ProviderIdentity, error) {
	for _, comment := range remote {
		if comment.IdempotencyKey == mutation.IdempotencyKey {
			mutation.ProviderIdentity = comment.ProviderIdentity
			if updateErr := integration.gateway.UpdateComment(ctx, target, mutation); updateErr != nil {
				return ProviderIdentity{}, updateErr
			}
			return comment.ProviderIdentity, nil
		}
	}
	return integration.gateway.CreateComment(ctx, target, mutation)
}

func classifyProviderError(err error) (bool, time.Duration) {
	var providerError *ProviderError
	if errors.As(err, &providerError) {
		return providerError.retryable, providerError.retryAfter
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true, 0
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true, 0
	}
	return false, 0
}

func publicationBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Minute
	for index := 1; index < attempt && delay < time.Hour; index++ {
		delay *= 2
	}
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func mutationFromComment(comment Comment) RemoteMutation {
	return RemoteMutation{CommentID: comment.ID, ParentCommentID: comment.ParentCommentID, ProviderIdentity: comment.ProviderIdentity, Body: comment.Body, Scope: comment.Scope, Path: comment.Path, OldPath: comment.OldPath, Side: comment.Side, Line: cloneLine(comment.Line), DiffHunk: comment.DiffHunk, OriginalHeadCommit: comment.OriginalHeadCommit}
}
