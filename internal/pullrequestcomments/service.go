package pullrequestcomments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sort"
	"strings"
	"time"
)

const MaximumBodyLength = 4000

type Service struct {
	locks           *keyedLocks
	remoteLocks     *keyedLocks
	publicationWake chan struct{}
	comments        Repository
	pullRequests    PullRequestTargetReader
	workers         WorkerReferenceReader
	now             func() time.Time
	newID           func() (string, error)
	provider        *providerIntegration
	refreshes       *commentRefreshes
}

func NewService(comments Repository, pullRequests PullRequestTargetReader, optionFunctions ...ServiceOption) *Service {
	service := &Service{
		locks: newKeyedLocks(), remoteLocks: newKeyedLocks(), publicationWake: make(chan struct{}, 1), comments: comments, pullRequests: pullRequests,
		now: func() time.Time { return time.Now().UTC() }, newID: newCommentID,
	}
	var options serviceOptions
	for _, option := range optionFunctions {
		if option != nil {
			option(&options)
		}
	}
	service.workers = options.workerReferences
	service.refreshes = newCommentRefreshes(options.scheduler, options.clock)
	service.provider = newProviderIntegration(comments, options.gateway, options.authors, service.now)
	if service.provider != nil {
		service.provider.localLocks = service.locks
		service.provider.report = func(err error) {
			if err != nil {
				log.Printf("PR comment publication: %v", err)
			}
		}
	}
	return service
}

func (service *Service) Create(ctx context.Context, request CreateComment) (Comment, error) {
	request.PullRequestID = strings.TrimSpace(request.PullRequestID)
	body := strings.TrimSpace(request.Body)
	if request.PullRequestID == "" || body == "" || len(body) > MaximumBodyLength {
		return Comment{}, ErrInvalidComment
	}
	unlock := service.locks.lock(request.PullRequestID)
	defer unlock()
	if request.Origin.sourceWorkerID != "" {
		existing, err := service.comments.FindBySourceWorkerID(ctx, request.Origin.sourceWorkerID)
		if err == nil {
			return publicComment(existing), nil
		}
		if !errors.Is(err, ErrCommentNotFound) {
			return Comment{}, err
		}
	}
	target, err := service.pullRequests.GetCommentTarget(ctx, request.PullRequestID)
	if err != nil {
		return Comment{}, err
	}
	if target.ID == "" {
		return Comment{}, ErrPullRequestNotFound
	}
	if request.Draft && target.Status != PullRequestWIP && target.Status != PullRequestDraft && target.Status != PullRequestOpen {
		return Comment{}, ErrReadOnly
	}
	if request.Origin.authorType == AuthorUser && request.Origin.authorGitHubUserID == "" && service.provider != nil {
		if authors, ok := service.provider.authors.(interface {
			CurrentCommentAuthor(context.Context, string) (string, error)
		}); ok {
			request.Origin.authorGitHubUserID, _ = authors.CurrentCommentAuthor(ctx, target.RepositoryID)
		}
	}
	var parent Comment
	if request.ParentCommentID != "" {
		parent, err = service.comments.Get(ctx, request.ParentCommentID)
		if err != nil {
			if errors.Is(err, ErrCommentNotFound) {
				return Comment{}, ErrInvalidParent
			}
			return Comment{}, err
		}
		if parent.PullRequestID != target.ID || parent.ParentCommentID != "" {
			return Comment{}, ErrInvalidParent
		}
		if parent.PublicationState == PublicationDraft {
			request.Draft = true
		}
	}
	id, err := service.newID()
	if err != nil {
		return Comment{}, err
	}
	now := service.now().UTC()
	scope := request.Scope
	if scope == "" {
		if parent.ID != "" {
			scope = parent.Scope
		} else {
			scope = ScopePullRequest
		}
	}
	if parent.ID != "" {
		if request.Path == "" {
			request.Path = parent.Path
		}
		if request.OldPath == "" {
			request.OldPath = parent.OldPath
		}
		if request.Side == "" {
			request.Side = parent.Side
		}
		if request.Line == nil {
			request.Line = cloneLine(parent.Line)
		}
		if request.DiffHunk == "" {
			request.DiffHunk = parent.DiffHunk
		}
	}
	status := Unresolved
	var resolvedAt *time.Time
	if parent.ID != "" || request.Origin.sourceWorkerID != "" {
		status = Resolved
		resolved := now
		resolvedAt = &resolved
	}
	headCommit := strings.TrimSpace(request.Origin.originalHeadCommit)
	if headCommit == "" {
		headCommit = target.HeadCommit
	}
	authorType := request.Origin.authorType
	if authorType == "" {
		authorType = AuthorUser
	}
	comment := Comment{
		ID: id, PullRequestID: target.ID, ParentCommentID: request.ParentCommentID, Body: body,
		Scope: scope, Path: request.Path, OldPath: request.OldPath, Side: request.Side,
		Line: cloneLine(request.Line), DiffHunk: request.DiffHunk, OriginalHeadCommit: headCommit,
		Status: status, ResolvedAt: resolvedAt, AuthorType: authorType,
		AuthorGitHubUserID: request.Origin.authorGitHubUserID,
		SourceSessionID:    request.Origin.sourceSessionID, SourceReviewID: request.Origin.sourceReviewID,
		SourceWorkerID: request.Origin.sourceWorkerID, PublicationState: PublicationLocal,
		CreatedAt: now, UpdatedAt: now,
	}
	if request.Draft {
		if authorType != AuthorUser {
			return Comment{}, ErrInvalidComment
		}
		comment.PublicationState = PublicationDraft
	}
	if err := service.saveComment(ctx, target, &comment, true, "create"); err != nil {
		if request.Origin.sourceWorkerID != "" {
			existing, getErr := service.comments.FindBySourceWorkerID(ctx, request.Origin.sourceWorkerID)
			if getErr == nil {
				return publicComment(existing), nil
			}
		}
		return Comment{}, err
	}
	return publicComment(comment), nil
}

// CompleteWorkerReply records a successfully completed worker's reply and resolves
// its thread. Reply delivery can be replayed after a restart; a later user change
// to the thread must take precedence over that older completion.
func (service *Service) CompleteWorkerReply(ctx context.Context, request CreateComment) (Comment, error) {
	if request.Origin.sourceWorkerID == "" {
		return Comment{}, ErrInvalidComment
	}
	reply, err := service.Create(ctx, request)
	if err != nil || reply.ParentCommentID == "" {
		return reply, err
	}
	unlock := service.locks.lock(reply.PullRequestID)
	defer unlock()
	parent, err := service.comments.Get(ctx, reply.ParentCommentID)
	if err != nil {
		return reply, err
	}
	if parent.Status == Resolved || parent.UpdatedAt.After(reply.CreatedAt) {
		return reply, nil
	}
	_, err = service.setStatusLocked(ctx, parent, Resolved)
	return reply, err
}

func (service *Service) CreateReviewComments(ctx context.Context, request CreateReviewComments) ([]Comment, error) {
	if err := ValidateReviewComments(request); err != nil {
		return nil, err
	}
	request.PullRequestID = strings.TrimSpace(request.PullRequestID)
	comments := append([]ReviewComment(nil), request.Comments...)
	for i := range comments {
		comments[i].Body = strings.TrimSpace(comments[i].Body)
	}
	unlock := service.locks.lock(request.PullRequestID)
	defer unlock()
	target, err := service.pullRequests.GetCommentTarget(ctx, request.PullRequestID)
	if err != nil {
		return nil, err
	}
	if target.ID == "" {
		return nil, ErrPullRequestNotFound
	}
	headCommit := strings.TrimSpace(request.Origin.originalHeadCommit)
	if headCommit == "" {
		headCommit = target.HeadCommit
	}
	result := make([]Comment, 0, len(comments))
	for index, input := range comments {
		comment := Comment{
			PullRequestID: target.ID, Body: input.Body, Scope: input.Scope,
			Path: input.Path, Side: input.Side, Line: cloneLine(input.Line),
			OriginalHeadCommit: headCommit, Status: Unresolved, AuthorType: AuthorAgent,
			SourceSessionID: request.Origin.sourceSessionID, SourceReviewID: request.Origin.sourceReviewID,
			SourceReviewIndex: index, PublicationState: PublicationLocal,
		}
		if request.Draft {
			comment.PublicationState = PublicationDraft
		}
		existing, imported, err := service.importedReviewComment(ctx, comment)
		if err != nil {
			return nil, err
		}
		if imported {
			if existing.ID != "" {
				if !request.Draft && existing.PublicationState == PublicationDraft {
					existing.PublicationState = PublicationLocal
					if err := service.saveComment(ctx, target, &existing, false, "create"); err != nil {
						return nil, err
					}
				}
				result = append(result, publicComment(existing))
			}
			continue
		}
		comment.ID, err = service.newID()
		if err != nil {
			return nil, err
		}
		comment.CreatedAt = service.now().UTC()
		comment.UpdatedAt = comment.CreatedAt
		if err := service.saveComment(ctx, target, &comment, true, "create"); err != nil {
			existing, imported, getErr := service.importedReviewComment(ctx, comment)
			if getErr != nil {
				return nil, getErr
			}
			if imported {
				if existing.ID != "" {
					result = append(result, publicComment(existing))
				}
				continue
			}
			return nil, err
		}
		result = append(result, publicComment(comment))
	}
	return result, nil
}

func validReviewCommentLocation(comment ReviewComment) bool {
	switch comment.Scope {
	case ScopePullRequest:
		return comment.Path == "" && comment.Side == "" && comment.Line == nil
	case ScopeFile:
		return strings.TrimSpace(comment.Path) != "" && comment.Side == "" && comment.Line == nil
	case ScopeLine:
		return strings.TrimSpace(comment.Path) != "" && (comment.Side == "LEFT" || comment.Side == "RIGHT") && comment.Line != nil && *comment.Line > 0
	default:
		return false
	}
}

// importedReviewComment validates the original import, then returns the current
// visible comment. A deleted comment is still imported, but omitted from results.
func (service *Service) importedReviewComment(ctx context.Context, expected Comment) (Comment, bool, error) {
	receipt, err := service.comments.FindReviewImportReceipt(ctx, expected.SourceReviewID, expected.SourceReviewIndex)
	if errors.Is(err, ErrCommentNotFound) {
		return Comment{}, false, nil
	}
	if err != nil {
		return Comment{}, false, err
	}
	if receipt.PullRequestID != expected.PullRequestID || receipt.Body != expected.Body || receipt.Scope != expected.Scope ||
		receipt.Path != expected.Path || receipt.Side != expected.Side || !sameLine(receipt.Line, expected.Line) ||
		receipt.OriginalHeadCommit != expected.OriginalHeadCommit || receipt.SourceSessionID != expected.SourceSessionID {
		return Comment{}, true, ErrInvalidComment
	}
	comment, err := service.comments.Get(ctx, receipt.ID)
	if errors.Is(err, ErrCommentNotFound) {
		return Comment{}, true, nil
	}
	return comment, true, err
}

func sameLine(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (service *Service) Get(ctx context.Context, id string) (Comment, error) {
	comment, err := service.comments.Get(ctx, id)
	if err != nil {
		return Comment{}, err
	}
	return publicComment(comment), nil
}

func (service *Service) ListByPullRequest(ctx context.Context, pullRequestID string) ([]Comment, error) {
	comments, err := service.comments.ListByPullRequest(ctx, pullRequestID)
	if err != nil {
		return nil, err
	}
	result := make([]Comment, len(comments))
	for index := range comments {
		result[index] = publicComment(comments[index])
	}
	sortComments(result)
	return result, nil
}

func (service *Service) UpdateBody(ctx context.Context, id, body string) (Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > MaximumBodyLength {
		return Comment{}, ErrInvalidComment
	}
	discovered, err := service.comments.Get(ctx, id)
	if err != nil {
		return Comment{}, err
	}
	remoteUnlock, err := service.remoteLocks.lockContext(ctx, discovered.PullRequestID)
	if err != nil {
		return Comment{}, err
	}
	defer remoteUnlock()
	unlock := service.locks.lock(discovered.PullRequestID)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	comment, err := service.comments.Get(ctx, id)
	if err != nil {
		return Comment{}, err
	}
	comment.Body = body
	comment.UpdatedAt = service.now().UTC()
	target, err := service.pullRequests.GetCommentTarget(ctx, comment.PullRequestID)
	if err != nil {
		return Comment{}, err
	}
	if err := service.saveComment(ctx, target, &comment, false, "update"); err != nil {
		return Comment{}, err
	}
	unlock()
	locked = false
	service.flushTarget(ctx, target)
	return service.Get(ctx, id)
}

func (service *Service) Delete(ctx context.Context, id string) error {
	discovered, err := service.comments.Get(ctx, id)
	if err != nil {
		return err
	}
	remoteUnlock, err := service.remoteLocks.lockContext(ctx, discovered.PullRequestID)
	if err != nil {
		return err
	}
	defer remoteUnlock()
	unlock := service.locks.lock(discovered.PullRequestID)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	comment, err := service.comments.Get(ctx, id)
	if err != nil {
		return err
	}
	var target PullRequestTarget
	if service.provider != nil {
		target, _ = service.pullRequests.GetCommentTarget(ctx, comment.PullRequestID)
	}
	if err := service.comments.Delete(ctx, id); err != nil {
		return err
	}
	if service.provider != nil {
		if err := service.provider.cancelCommentPublications(ctx, id); err != nil {
			return err
		}
		job, err := service.provider.mutationJob(ctx, target, "delete", comment)
		if err != nil {
			return err
		}
		if job != nil {
			if err := service.provider.publications.EnqueuePublication(ctx, *job); err != nil {
				return err
			}
			service.notifyPublication()
		}
	}
	unlock()
	locked = false
	service.flushTarget(ctx, target)
	return nil
}

func (service *Service) Resolve(ctx context.Context, id string) (Comment, error) {
	return service.setStatus(ctx, id, Resolved)
}

func (service *Service) Reopen(ctx context.Context, id string) (Comment, error) {
	return service.setStatus(ctx, id, Unresolved)
}

func (service *Service) setStatus(ctx context.Context, id string, status Status) (Comment, error) {
	discovered, err := service.comments.Get(ctx, id)
	if err != nil {
		return Comment{}, err
	}
	remoteUnlock, err := service.remoteLocks.lockContext(ctx, discovered.PullRequestID)
	if err != nil {
		return Comment{}, err
	}
	defer remoteUnlock()
	unlock := service.locks.lock(discovered.PullRequestID)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	comment, err := service.comments.Get(ctx, id)
	if err != nil {
		return Comment{}, err
	}
	updated, err := service.setStatusLocked(ctx, comment, status)
	if err != nil {
		return Comment{}, err
	}
	unlock()
	locked = false
	if target, err := service.pullRequests.GetCommentTarget(ctx, comment.PullRequestID); err == nil {
		service.flushTarget(ctx, target)
	}
	if latest, err := service.Get(ctx, id); err == nil {
		return latest, nil
	}
	return updated, nil
}

func (service *Service) setStatusLocked(ctx context.Context, comment Comment, status Status) (Comment, error) {
	if comment.ParentCommentID != "" {
		return Comment{}, ErrInvalidComment
	}
	now := service.now().UTC()
	comment.Status = status
	comment.UpdatedAt = now
	if status == Resolved {
		comment.ResolvedAt = &now
	} else {
		comment.ResolvedAt = nil
	}
	target, err := service.pullRequests.GetCommentTarget(ctx, comment.PullRequestID)
	if err != nil {
		return Comment{}, err
	}
	operation := "reopen"
	if status == Resolved {
		operation = "resolve"
	}
	if err := service.saveComment(ctx, target, &comment, false, operation); err != nil {
		return Comment{}, err
	}
	return publicComment(comment), nil
}

func (service *Service) UnresolvedCount(ctx context.Context, pullRequestID string) (int, error) {
	return service.comments.UnresolvedCount(ctx, pullRequestID)
}

func (service *Service) UnresolvedCounts(ctx context.Context, pullRequestIDs []string) (map[string]int, error) {
	return service.comments.UnresolvedCounts(ctx, pullRequestIDs)
}

func (service *Service) synchronize(ctx context.Context, pullRequestID string) ([]Comment, error) {
	remoteUnlock, err := service.remoteLocks.lockContext(ctx, pullRequestID)
	if err != nil {
		return nil, err
	}
	defer remoteUnlock()
	target, err := service.pullRequests.GetCommentTarget(ctx, pullRequestID)
	if err != nil {
		return nil, err
	}
	providerTarget, eligible := providerTarget(target)
	if service.provider == nil || !eligible || (target.Status != PullRequestDraft && target.Status != PullRequestOpen) {
		return service.ListByPullRequest(ctx, pullRequestID)
	}
	service.provider.flush(ctx, providerTarget)
	remote, err := service.provider.gateway.ListComments(ctx, providerTarget)
	if err != nil {
		return nil, err
	}
	unlock := service.locks.lock(pullRequestID)
	defer unlock()
	pendingCommentIDs, err := service.provider.pendingCommentIDs(ctx, pullRequestID)
	if err != nil {
		return nil, err
	}
	existing, err := service.comments.ListByPullRequest(ctx, pullRequestID)
	if err != nil {
		return nil, err
	}
	byExternal := make(map[string]Comment, len(existing)+len(remote))
	for _, comment := range existing {
		if comment.ProviderIdentity.Provider == target.SyncProvider && comment.ProviderIdentity.ExternalID != "" {
			byExternal[comment.ProviderIdentity.ExternalID] = comment
		}
	}
	newExternal := make(map[string]bool, len(remote))
	for _, incoming := range remote {
		externalID := incoming.ProviderIdentity.ExternalID
		if externalID == "" {
			continue
		}
		if _, exists := byExternal[externalID]; exists {
			continue
		}
		id, idErr := service.newID()
		if idErr != nil {
			return nil, idErr
		}
		byExternal[externalID] = Comment{ID: id}
		newExternal[externalID] = true
	}
	seen := make(map[string]bool, len(remote))
	for _, incoming := range parentsBeforeChildren(remote) {
		if incoming.ProviderIdentity.Provider == "" {
			incoming.ProviderIdentity.Provider = target.SyncProvider
		}
		if incoming.ProviderIdentity.ExternalID == "" {
			continue
		}
		seen[incoming.ProviderIdentity.ExternalID] = true
		comment, exists := byExternal[incoming.ProviderIdentity.ExternalID]
		isNew := newExternal[incoming.ProviderIdentity.ExternalID]
		if exists && !isNew && pendingCommentIDs[comment.ID] {
			continue
		}
		parentID := ""
		if incoming.ParentExternalID != "" {
			if parent, ok := byExternal[incoming.ParentExternalID]; ok {
				parentID = parent.ID
			}
		} else if !isNew && comment.ParentCommentID != "" {
			parentID = comment.ParentCommentID
		}
		if isNew {
			comment.CreatedAt = incoming.CreatedAt.UTC()
			if comment.CreatedAt.IsZero() {
				comment.CreatedAt = service.now().UTC()
			}
			comment.Status = Unresolved
		}
		if incoming.ProviderIdentity.Kind == "inline" && incoming.ParentExternalID == "" {
			comment.Status = Unresolved
			comment.ResolvedAt = nil
			if incoming.Resolved {
				comment.Status = Resolved
				resolved := incoming.UpdatedAt.UTC()
				if resolved.IsZero() {
					resolved = service.now().UTC()
				}
				comment.ResolvedAt = &resolved
			}
		}
		comment.PullRequestID = pullRequestID
		comment.Body = incoming.Body
		comment.Scope, comment.Path, comment.OldPath, comment.Side = incoming.Scope, incoming.Path, incoming.OldPath, incoming.Side
		if comment.Scope == "" {
			comment.Scope = ScopePullRequest
		}
		comment.Line, comment.DiffHunk = cloneLine(incoming.Line), incoming.DiffHunk
		comment.OriginalHeadCommit = incoming.OriginalHeadCommit
		if comment.OriginalHeadCommit == "" {
			comment.OriginalHeadCommit = target.HeadCommit
		}
		if isNew {
			comment.AuthorType = AuthorUser
		}
		if comment.AuthorType == AuthorUser && comment.AuthorGitHubUserID == "" && service.provider.authors != nil && incoming.Author.ExternalID != "" {
			if authorID, observeErr := service.provider.authors.ObserveCommentAuthor(ctx, target.RepositoryID, incoming.Author); observeErr != nil {
				return nil, observeErr
			} else {
				comment.AuthorGitHubUserID = authorID
			}
		}
		comment.ProviderIdentity, comment.PublicationState = incoming.ProviderIdentity, PublicationPublished
		comment.UpdatedAt = incoming.UpdatedAt.UTC()
		if comment.UpdatedAt.IsZero() {
			comment.UpdatedAt = comment.CreatedAt
		}
		if parentID != "" {
			comment.ParentCommentID = parentID
			comment.Status = Resolved
			resolved := incoming.UpdatedAt.UTC()
			if resolved.IsZero() {
				resolved = service.now().UTC()
			}
			comment.ResolvedAt = &resolved
		}
		stored, upsertErr := service.comments.UpsertExternal(ctx, comment)
		if upsertErr != nil {
			return nil, upsertErr
		}
		byExternal[incoming.ProviderIdentity.ExternalID] = stored
	}
	for _, comment := range existing {
		if comment.ProviderIdentity.Provider != target.SyncProvider || comment.ProviderIdentity.ExternalID == "" || seen[comment.ProviderIdentity.ExternalID] || pendingCommentIDs[comment.ID] {
			continue
		}
		hasReplies, referenceErr := service.comments.HasChildReplies(ctx, comment.ID)
		if referenceErr != nil {
			return nil, referenceErr
		}
		if hasReplies || service.workers == nil {
			continue
		}
		hasWorkers, referenceErr := service.workers.HasWorkerReference(ctx, comment.ID)
		if referenceErr != nil {
			return nil, referenceErr
		}
		if !hasWorkers {
			if deleteErr := service.comments.Delete(ctx, comment.ID); deleteErr != nil && !errors.Is(deleteErr, ErrCommentNotFound) {
				return nil, deleteErr
			}
		}
	}
	return service.comments.ListByPullRequest(ctx, pullRequestID)
}

func parentsBeforeChildren(comments []RemoteComment) []RemoteComment {
	byExternal := make(map[string]int, len(comments))
	for index, comment := range comments {
		if comment.ProviderIdentity.ExternalID != "" {
			byExternal[comment.ProviderIdentity.ExternalID] = index
		}
	}
	ordered := make([]RemoteComment, 0, len(comments))
	state := make([]uint8, len(comments))
	var visit func(int)
	visit = func(index int) {
		if state[index] == 2 {
			return
		}
		if state[index] == 1 {
			state[index] = 2
			ordered = append(ordered, comments[index])
			return
		}
		state[index] = 1
		if parentIndex, ok := byExternal[comments[index].ParentExternalID]; ok && state[parentIndex] == 0 {
			visit(parentIndex)
		}
		if state[index] != 2 {
			state[index] = 2
			ordered = append(ordered, comments[index])
		}
	}
	for index := range comments {
		visit(index)
	}
	return ordered
}

func newCommentID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "prc-" + hex.EncodeToString(value), nil
}

func publicComment(comment Comment) Comment {
	comment.Line = cloneLine(comment.Line)
	if comment.ResolvedAt != nil {
		resolved := comment.ResolvedAt.UTC()
		comment.ResolvedAt = &resolved
	}
	comment.CreatedAt = comment.CreatedAt.UTC()
	comment.UpdatedAt = comment.UpdatedAt.UTC()
	return comment
}

func cloneLine(line *int) *int {
	if line == nil {
		return nil
	}
	value := *line
	return &value
}

func sortComments(comments []Comment) {
	sort.Slice(comments, func(i, j int) bool {
		if comments[i].CreatedAt.Equal(comments[j].CreatedAt) {
			return comments[i].ID < comments[j].ID
		}
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})
}

// saveComment is called under the PR local-state lock. Provider I/O is never part of a save.
func (service *Service) saveComment(ctx context.Context, target PullRequestTarget, comment *Comment, insert bool, operation string) error {
	if service.provider == nil || comment.PublicationState == PublicationDraft {
		if insert {
			return service.comments.Insert(ctx, *comment)
		}
		return service.comments.Update(ctx, *comment)
	}
	job, err := service.provider.mutationJob(ctx, target, operation, *comment)
	if err != nil {
		return err
	}
	if job != nil {
		comment.PublicationState = PublicationPending
	}
	if err := service.provider.publications.SaveWithPublication(ctx, *comment, insert, job); err != nil {
		return err
	}
	if job != nil {
		service.notifyPublication()
	}
	return nil
}

func (service *Service) notifyPublication() {
	select {
	case service.publicationWake <- struct{}{}:
	default:
	}
}

// flushTarget requires the remote lock, and must run outside the local-state lock.
func (service *Service) flushTarget(ctx context.Context, target PullRequestTarget) {
	if service.provider != nil {
		if remote, ok := providerTarget(target); ok {
			service.provider.flush(ctx, remote)
		}
	}
}

// ValidateReviewComments checks the entire batch without creating comments or jobs.
func ValidateReviewComments(request CreateReviewComments) error {
	request.PullRequestID = strings.TrimSpace(request.PullRequestID)
	if request.PullRequestID == "" || request.Origin.sourceReviewID == "" || request.Origin.sourceSessionID == "" {
		return ErrInvalidComment
	}
	for _, comment := range request.Comments {
		comment.Body = strings.TrimSpace(comment.Body)
		if comment.Body == "" || len(comment.Body) > MaximumBodyLength || !validReviewCommentLocation(comment) {
			return ErrInvalidComment
		}
	}
	return nil
}
