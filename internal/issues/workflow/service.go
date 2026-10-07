// Package workflow coordinates the public issue workflow across projects,
// GitHub transport, identity resolution, and the local issue projection.
package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/githubidentity"
	"github.com/holark-ai/holark/internal/issues"
	"github.com/holark-ai/holark/internal/issues/comments"
)

var (
	ErrInvalidLabelRequest      = errors.New("invalid label request")
	ErrLabelNotFound            = errors.New("label not found")
	ErrLabelAlreadyExists       = errors.New("label already exists")
	ErrProjectNotFound          = errors.New("project not found")
	ErrRepositoryUnsupported    = errors.New("issue repository is unsupported")
	ErrIssueSourceUnavailable   = errors.New("issue source is unavailable")
	ErrIssueSourceFailed        = errors.New("issue source failed")
	ErrMutationAccepted         = errors.New("issue mutation was accepted")
	ErrIssueAssigneesReadOnly   = errors.New("issue assignees are read-only")
	ErrProviderIdentityRequired = errors.New("provider identity is required")
	ErrProjectMemberUnresolved  = errors.New("project member could not be resolved")
)

type MutationOperation string

const (
	MutationCreate           MutationOperation = "create"
	MutationUpdate           MutationOperation = "update"
	MutationClose            MutationOperation = "close"
	MutationReopen           MutationOperation = "reopen"
	MutationAddLabels        MutationOperation = "add_labels"
	MutationRemoveLabels     MutationOperation = "remove_labels"
	MutationReplaceAssignees MutationOperation = "replace_assignees"
	MutationAddAssignee      MutationOperation = "add_assignee"
	MutationRemoveAssignee   MutationOperation = "remove_assignee"
)

type Project struct {
	ID            string
	RepositoryURL string
	DefaultBranch string
}

type ProjectLookup func(string) (Project, bool)

type RemoteReference struct {
	URL    string
	Number int
}

type RemoteIssue struct {
	Members               []githubidentity.SourceMember
	Reference             RemoteReference
	Issue                 issues.Issue
	CreatorGitHubNodeID   string
	AssigneeGitHubNodeIDs []string
}

type UpdateRequest struct {
	Title *string
	Body  *string
	State *string
}

type Transport interface {
	Snapshot(context.Context, string, string, time.Time) ([]RemoteIssue, error)
	Create(context.Context, string, string, string, string, time.Time) (RemoteIssue, error)
	Update(context.Context, issues.Issue, string, UpdateRequest, time.Time) (RemoteIssue, error)
	ReplaceAssignees(context.Context, issues.Issue, string, []string, time.Time) (RemoteIssue, error)
	AddAssignee(context.Context, issues.Issue, string, string, time.Time) (RemoteIssue, error)
	RemoveAssignee(context.Context, issues.Issue, string, string, time.Time) (RemoteIssue, error)
}

// OpenTransport supports incremental refreshes without deleting historical issues.
type OpenTransport interface {
	SnapshotOpen(context.Context, string, string, time.Time) ([]RemoteIssue, error)
	Get(context.Context, issues.Issue, string, time.Time) (RemoteIssue, error)
}

type LabelTransport interface {
	ListLabels(context.Context, string, string) ([]issues.Label, error)
	CreateLabel(context.Context, string, string, string, string, string) (issues.Label, error)
	ListIssueLabels(context.Context, issues.Issue, string) ([]issues.Label, error)
	AddIssueLabels(context.Context, issues.Issue, string, []string) ([]issues.Label, error)
	ReplaceIssueLabels(context.Context, issues.Issue, string, []string) ([]issues.Label, error)
}

type Projection interface {
	Get(context.Context, string) (issues.Issue, error)
	List(context.Context, string) ([]issues.Issue, error)
	StoreSynced(context.Context, string, issues.Issue) (issues.Issue, bool, error)
	DeleteSyncedAbsent(context.Context, string, string, []string) (int, error)
}

type LabelProjection interface {
	UpsertLabelCatalog(context.Context, string, []issues.Label) ([]issues.Label, error)
}

type CreateLabelRequest struct {
	Name        string
	Color       string
	Description string
}

type IdentityResolver interface {
	ResolveProjectMemberIDs(context.Context, string, []string) ([]string, error)
	ResolveProjectMemberLogins(context.Context, string, []string) ([]string, error)
}

type Snapshot struct {
	Discussion   comments.Context `json:"discussion"`
	IssueID      string           `json:"issue_id"`
	RepositoryID string           `json:"repository_id"`
	Title        string           `json:"title"`
	Body         string           `json:"body"`
}

type SyncResult struct {
	Issues   []issues.Issue
	Imported int
	Updated  int
	Exported int
	SyncedAt time.Time
}

type ProjectionPendingError struct {
	GitHubIssueURL    string
	GitHubIssueNumber int
	Operation         MutationOperation
	SafeToRetry       bool
	Err               error
}

type LabelProjectionPendingError struct{ Err error }

func (err *LabelProjectionPendingError) Error() string {
	if err.Err != nil {
		return err.Err.Error()
	}
	return "label projection is pending"
}

func (err *LabelProjectionPendingError) Unwrap() error { return err.Err }

func (err *ProjectionPendingError) Error() string {
	if err.Err != nil {
		return err.Err.Error()
	}
	return "issue projection is pending"
}

func (err *ProjectionPendingError) Unwrap() error { return err.Err }

type issueLock struct {
	sync.Mutex
	references int
}

type SyncRecorder interface {
	RecordSync(context.Context, string, string, error) error
}

type Service struct {
	syncRecorder SyncRecorder
	comments     comments.Store
	projects     ProjectLookup
	transport    Transport
	projection   Projection
	identities   IdentityResolver
	now          func() time.Time
	random       io.Reader
	lockMu       sync.Mutex
	issueLocks   map[string]*issueLock
	projectLocks map[string]*sync.RWMutex
}

func New(projects ProjectLookup, transport Transport, projection Projection, identities IdentityResolver) *Service {
	return &Service{
		projects: projects, transport: transport, projection: projection, identities: identities,
		now: func() time.Time { return time.Now().UTC() }, random: rand.Reader,
		issueLocks:   make(map[string]*issueLock),
		projectLocks: make(map[string]*sync.RWMutex),
	}
}

func (service *Service) List(ctx context.Context, projectSlug string) ([]issues.Issue, error) {
	project, ok := service.projects(projectSlug)
	if !ok {
		return nil, ErrProjectNotFound
	}
	return service.projection.List(ctx, project.ID)
}

func (service *Service) Get(ctx context.Context, id string) (issues.Issue, error) {
	return service.projection.Get(ctx, id)
}

func (service *Service) ListLabels(ctx context.Context, projectSlug string) ([]issues.Label, error) {
	project, ok := service.projects(projectSlug)
	if !ok {
		return nil, ErrProjectNotFound
	}
	return service.refreshLabels(ctx, project)
}

func (service *Service) CreateLabel(ctx context.Context, projectSlug string, request CreateLabelRequest) (issues.Label, error) {
	project, ok := service.projects(projectSlug)
	if !ok {
		return issues.Label{}, ErrProjectNotFound
	}
	name := strings.TrimSpace(request.Name)
	description := request.Description
	if name == "" || utf8.RuneCountInString(description) > 100 {
		return issues.Label{}, ErrInvalidLabelRequest
	}
	color := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(request.Color), "#"))
	if color == "" {
		value := make([]byte, 3)
		if _, err := io.ReadFull(service.random, value); err != nil {
			return issues.Label{}, err
		}
		color = hex.EncodeToString(value)
	}
	if !validLabelColor(color) {
		return issues.Label{}, ErrInvalidLabelRequest
	}
	catalog, err := service.refreshLabels(ctx, project)
	if err != nil {
		return issues.Label{}, err
	}
	for _, label := range catalog {
		if strings.EqualFold(label.Name, name) {
			return issues.Label{}, ErrLabelAlreadyExists
		}
	}
	transport, projection, err := service.labelDependencies()
	if err != nil {
		return issues.Label{}, err
	}
	created, err := transport.CreateLabel(ctx, project.ID, project.RepositoryURL, name, color, description)
	if err != nil {
		if errors.Is(err, ErrMutationAccepted) {
			return issues.Label{}, &LabelProjectionPendingError{Err: err}
		}
		return issues.Label{}, err
	}
	stored, err := projection.UpsertLabelCatalog(ctx, project.ID, []issues.Label{created})
	if err != nil {
		return issues.Label{}, &LabelProjectionPendingError{Err: err}
	}
	return stored[0], nil
}

func (service *Service) AddLabels(ctx context.Context, issueID string, names []string) (issues.Issue, error) {
	requested, err := normalizeRequestedNames(names)
	if err != nil {
		return issues.Issue{}, err
	}
	unlock := service.lockIssue(issueID)
	defer unlock()
	stored, project, unlockProject, err := service.loadIssueForMutation(ctx, issueID)
	if err != nil {
		return issues.Issue{}, err
	}
	defer unlockProject()
	transport, _, err := service.labelDependencies()
	if err != nil {
		return issues.Issue{}, err
	}
	catalog, err := service.refreshLabels(ctx, project)
	if err != nil {
		return issues.Issue{}, err
	}
	resolved := make([]string, 0, len(requested))
	for _, name := range requested {
		canonicalName := ""
		for _, label := range catalog {
			if strings.EqualFold(label.Name, name) {
				canonicalName = label.Name
				break
			}
		}
		if canonicalName == "" {
			return issues.Issue{}, ErrLabelNotFound
		}
		resolved = append(resolved, canonicalName)
	}
	labels, err := transport.AddIssueLabels(ctx, stored, project.RepositoryURL, resolved)
	if err != nil {
		return issues.Issue{}, service.labelMutationError(stored, MutationAddLabels, err)
	}
	return service.storeLabelSnapshot(ctx, project.ID, stored, labels, MutationAddLabels)
}

func (service *Service) RemoveLabels(ctx context.Context, issueID string, names []string) (issues.Issue, error) {
	requested, err := normalizeRequestedNames(names)
	if err != nil {
		return issues.Issue{}, err
	}
	unlock := service.lockIssue(issueID)
	defer unlock()
	stored, project, unlockProject, err := service.loadIssueForMutation(ctx, issueID)
	if err != nil {
		return issues.Issue{}, err
	}
	defer unlockProject()
	transport, _, err := service.labelDependencies()
	if err != nil {
		return issues.Issue{}, err
	}
	current, err := transport.ListIssueLabels(ctx, stored, project.RepositoryURL)
	if err != nil {
		return issues.Issue{}, err
	}
	remaining := make([]string, 0, len(current))
	changed := false
	for _, label := range current {
		if containsFold(requested, strings.TrimSpace(label.Name)) {
			changed = true
			continue
		}
		remaining = append(remaining, label.Name)
	}
	result := current
	if changed {
		result, err = transport.ReplaceIssueLabels(ctx, stored, project.RepositoryURL, remaining)
		if err != nil {
			return issues.Issue{}, service.labelMutationError(stored, MutationRemoveLabels, err)
		}
	}
	return service.storeLabelSnapshot(ctx, project.ID, stored, result, MutationRemoveLabels)
}

func (service *Service) refreshLabels(ctx context.Context, project Project) ([]issues.Label, error) {
	transport, projection, err := service.labelDependencies()
	if err != nil {
		return nil, err
	}
	remote, err := transport.ListLabels(ctx, project.ID, project.RepositoryURL)
	if err != nil {
		return nil, err
	}
	stored, err := projection.UpsertLabelCatalog(ctx, project.ID, remote)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(stored, func(i, j int) bool { return strings.ToLower(stored[i].Name) < strings.ToLower(stored[j].Name) })
	if stored == nil {
		stored = []issues.Label{}
	}
	return stored, nil
}

func (service *Service) labelDependencies() (LabelTransport, LabelProjection, error) {
	transport, transportOK := service.transport.(LabelTransport)
	projection, projectionOK := service.projection.(LabelProjection)
	if !transportOK || !projectionOK {
		return nil, nil, ErrIssueSourceFailed
	}
	return transport, projection, nil
}

func (service *Service) storeLabelSnapshot(ctx context.Context, projectID string, stored issues.Issue, labels []issues.Label, operation MutationOperation) (issues.Issue, error) {
	stored.Labels = append([]issues.Label(nil), labels...)
	projected, _, err := service.projection.StoreSynced(ctx, projectID, stored)
	if err != nil {
		return issues.Issue{}, projectionPendingForIssue(operation, stored, true, err)
	}
	return projected, nil
}

func (service *Service) labelMutationError(stored issues.Issue, operation MutationOperation, err error) error {
	if errors.Is(err, ErrMutationAccepted) {
		return projectionPendingForIssue(operation, stored, true, err)
	}
	return err
}

func normalizeRequestedNames(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, raw := range values {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, ErrInvalidLabelRequest
		}
		if containsFold(result, name) {
			continue
		}
		result = append(result, name)
	}
	if len(result) == 0 {
		return nil, ErrInvalidLabelRequest
	}
	return result, nil
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func validLabelColor(color string) bool {
	if len(color) != 6 {
		return false
	}
	for _, character := range color {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func (service *Service) Snapshot(ctx context.Context, id string) (Snapshot, error) {
	issue, err := service.projection.Get(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	discussion := comments.Discussion{}
	if service.comments != nil {
		discussion, err = service.comments.ListComments(ctx, id)
		if err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{IssueID: issue.ID, RepositoryID: issue.RepositoryID, Title: issue.Title, Body: issue.Body, Discussion: comments.BuildContext(discussion)}, nil
}

func (service *Service) Create(ctx context.Context, projectSlug, title, body string) (issues.Issue, error) {
	project, ok := service.projects(projectSlug)
	if !ok {
		return issues.Issue{}, ErrProjectNotFound
	}
	unlockProject := service.lockProjectMutation(project.ID)
	defer unlockProject()
	remote, err := service.transport.Create(ctx, project.ID, project.RepositoryURL, title, body, service.now().UTC())
	if err != nil {
		if errors.Is(err, ErrMutationAccepted) {
			return issues.Issue{}, projectionPending(MutationCreate, remote, err)
		}
		return issues.Issue{}, err
	}
	projected, err := service.withResolvedIdentities(ctx, project.ID, remote)
	if err != nil {
		return issues.Issue{}, projectionPending(MutationCreate, remote, err)
	}
	stored, _, err := service.projection.StoreSynced(ctx, project.ID, projected)
	if err != nil {
		return issues.Issue{}, projectionPending(MutationCreate, remote, err)
	}
	return stored, nil
}

func (service *Service) Update(ctx context.Context, id string, title, body *string) (issues.Issue, error) {
	return service.mutate(ctx, id, MutationUpdate, UpdateRequest{Title: title, Body: body})
}

func (service *Service) Close(ctx context.Context, id string) (issues.Issue, error) {
	state := "closed"
	return service.mutate(ctx, id, MutationClose, UpdateRequest{State: &state})
}

func (service *Service) Reopen(ctx context.Context, id string) (issues.Issue, error) {
	state := "open"
	return service.mutate(ctx, id, MutationReopen, UpdateRequest{State: &state})
}

func (service *Service) ReplaceAssignees(ctx context.Context, id string, holarkIDs []string) (issues.Issue, error) {
	normalized, err := normalizeMemberIDs(holarkIDs)
	if err != nil {
		return issues.Issue{}, err
	}
	return service.mutateAssignees(ctx, id, MutationReplaceAssignees, normalized,
		func(ctx context.Context, stored issues.Issue, repositoryURL string, logins []string, at time.Time) (RemoteIssue, error) {
			return service.transport.ReplaceAssignees(ctx, stored, repositoryURL, logins, at)
		})
}

func (service *Service) AddAssignee(ctx context.Context, id, holarkID string) (issues.Issue, error) {
	normalized, err := normalizeMemberIDs([]string{holarkID})
	if err != nil || len(normalized) != 1 {
		return issues.Issue{}, ErrProjectMemberUnresolved
	}
	return service.mutateAssignees(ctx, id, MutationAddAssignee, normalized,
		func(ctx context.Context, stored issues.Issue, repositoryURL string, logins []string, at time.Time) (RemoteIssue, error) {
			return service.transport.AddAssignee(ctx, stored, repositoryURL, logins[0], at)
		})
}

func (service *Service) RemoveAssignee(ctx context.Context, id, holarkID string) (issues.Issue, error) {
	normalized, err := normalizeMemberIDs([]string{holarkID})
	if err != nil || len(normalized) != 1 {
		return issues.Issue{}, ErrProjectMemberUnresolved
	}
	return service.mutateAssignees(ctx, id, MutationRemoveAssignee, normalized,
		func(ctx context.Context, stored issues.Issue, repositoryURL string, logins []string, at time.Time) (RemoteIssue, error) {
			return service.transport.RemoveAssignee(ctx, stored, repositoryURL, logins[0], at)
		})
}

type assigneeTransportOperation func(context.Context, issues.Issue, string, []string, time.Time) (RemoteIssue, error)

func (service *Service) mutateAssignees(ctx context.Context, id string, operation MutationOperation, holarkIDs []string, mutate assigneeTransportOperation) (issues.Issue, error) {
	unlock := service.lockIssue(id)
	defer unlock()

	stored, project, unlockProject, err := service.loadIssueForMutation(ctx, id)
	if err != nil {
		return issues.Issue{}, err
	}
	defer unlockProject()
	if stored.Status != issues.IssueOpen {
		return issues.Issue{}, ErrIssueAssigneesReadOnly
	}
	if strings.TrimSpace(stored.SyncProvider) != string(issues.IssueSyncProviderGitHub) {
		return issues.Issue{}, ErrProviderIdentityRequired
	}
	if service.identities == nil {
		return issues.Issue{}, ErrProjectMemberUnresolved
	}
	logins, err := service.identities.ResolveProjectMemberLogins(ctx, project.ID, holarkIDs)
	if err != nil {
		return issues.Issue{}, fmt.Errorf("%w: %v", ErrProjectMemberUnresolved, err)
	}
	logins, err = normalizeResolvedValues(logins, len(holarkIDs))
	if err != nil {
		return issues.Issue{}, err
	}
	remote, err := mutate(ctx, stored, project.RepositoryURL, logins, service.now().UTC())
	if err != nil {
		if errors.Is(err, ErrMutationAccepted) {
			return issues.Issue{}, projectionPending(operation, remote, err)
		}
		return issues.Issue{}, err
	}
	if service.identities == nil {
		return issues.Issue{}, projectionPending(operation, remote, ErrProjectMemberUnresolved)
	}
	if err := service.observeMembers(ctx, project.ID, remote); err != nil {
		return issues.Issue{}, projectionPending(operation, remote, err)
	}
	resolved, err := service.identities.ResolveProjectMemberIDs(ctx, project.ID, remote.AssigneeGitHubNodeIDs)
	if err != nil {
		return issues.Issue{}, projectionPending(operation, remote, fmt.Errorf("%w: %v", ErrProjectMemberUnresolved, err))
	}
	resolved, err = normalizeResolvedValues(resolved, len(remote.AssigneeGitHubNodeIDs))
	if err != nil {
		return issues.Issue{}, projectionPending(operation, remote, err)
	}
	canonical := remote.Issue
	canonical.IssuerHolarkID = stored.IssuerHolarkID
	canonical.AssigneeHolarkIDs = resolved
	projected, _, err := service.projection.StoreSynced(ctx, project.ID, canonical)
	if err != nil {
		return issues.Issue{}, projectionPending(operation, remote, err)
	}
	return projected, nil
}

func (service *Service) mutate(ctx context.Context, id string, operation MutationOperation, request UpdateRequest) (issues.Issue, error) {
	unlock := service.lockIssue(id)
	defer unlock()
	stored, project, unlockProject, err := service.loadIssueForMutation(ctx, id)
	if err != nil {
		return issues.Issue{}, err
	}
	defer unlockProject()
	remote, err := service.transport.Update(ctx, stored, project.RepositoryURL, request, service.now().UTC())
	if err != nil {
		if errors.Is(err, ErrMutationAccepted) {
			return issues.Issue{}, projectionPending(operation, remote, err)
		}
		return issues.Issue{}, err
	}
	canonical := remote.Issue
	canonical.IssuerHolarkID = stored.IssuerHolarkID
	canonical.AssigneeHolarkIDs = append([]string(nil), stored.AssigneeHolarkIDs...)
	projected, _, err := service.projection.StoreSynced(ctx, project.ID, canonical)
	if err != nil {
		return issues.Issue{}, projectionPending(operation, remote, err)
	}
	return projected, nil
}

func (service *Service) lockIssue(id string) func() {
	key := strings.TrimSpace(id)
	service.lockMu.Lock()
	lock := service.issueLocks[key]
	if lock == nil {
		lock = &issueLock{}
		service.issueLocks[key] = lock
	}
	lock.references++
	service.lockMu.Unlock()
	lock.Lock()
	return func() {
		lock.Unlock()

		service.lockMu.Lock()
		defer service.lockMu.Unlock()
		lock.references--
		if lock.references == 0 {
			delete(service.issueLocks, key)
		}
	}
}

func (service *Service) loadIssueForMutation(ctx context.Context, id string) (issues.Issue, Project, func(), error) {
	for {
		stored, err := service.projection.Get(ctx, id)
		if err != nil {
			return issues.Issue{}, Project{}, nil, err
		}
		project, ok := service.projects(stored.RepositoryID)
		if !ok {
			return issues.Issue{}, Project{}, nil, ErrProjectNotFound
		}
		unlockProject := service.lockProjectMutation(project.ID)
		current, err := service.projection.Get(ctx, id)
		if err != nil {
			unlockProject()
			return issues.Issue{}, Project{}, nil, err
		}
		if current.RepositoryID != stored.RepositoryID {
			unlockProject()
			continue
		}
		return current, project, unlockProject, nil
	}
}

func (service *Service) projectLock(id string) *sync.RWMutex {
	key := strings.TrimSpace(id)
	service.lockMu.Lock()
	lock := service.projectLocks[key]
	if lock == nil {
		lock = &sync.RWMutex{}
		service.projectLocks[key] = lock
	}
	service.lockMu.Unlock()
	return lock
}

func (service *Service) lockProjectMutation(id string) func() {
	lock := service.projectLock(id)
	lock.RLock()
	return lock.RUnlock
}

func (service *Service) lockProjectSync(id string) func() {
	lock := service.projectLock(id)
	lock.Lock()
	return lock.Unlock
}

func normalizeMemberIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, ErrProjectMemberUnresolved
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func normalizeResolvedValues(values []string, expected int) ([]string, error) {
	if len(values) != expected {
		return nil, ErrProjectMemberUnresolved
	}
	result := make([]string, len(values))
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, ErrProjectMemberUnresolved
		}
		result[index] = value
	}
	return result, nil
}

func (service *Service) WithSyncRecorder(recorder SyncRecorder) { service.syncRecorder = recorder }

func (service *Service) Sync(ctx context.Context, projectSlug string) (result SyncResult, resultErr error) {
	defer func() {
		if service.syncRecorder != nil {
			resultErr = errors.Join(resultErr, service.syncRecorder.RecordSync(context.WithoutCancel(ctx), projectSlug, "issues", resultErr))
		}
	}()
	project, ok := service.projects(projectSlug)
	if !ok {
		return SyncResult{}, ErrProjectNotFound
	}
	unlockProject := service.lockProjectSync(project.ID)
	defer unlockProject()
	syncedAt := service.now().UTC()
	remote, err := service.transport.Snapshot(ctx, project.ID, project.RepositoryURL, syncedAt)
	if err != nil {
		return SyncResult{}, err
	}
	nodeIDs := make([]string, 0)
	for _, candidate := range remote {
		if err := service.observeMembers(ctx, project.ID, candidate); err != nil {
			return SyncResult{}, err
		}
		nodeIDs = append(nodeIDs, candidate.CreatorGitHubNodeID)
		nodeIDs = append(nodeIDs, candidate.AssigneeGitHubNodeIDs...)
	}
	resolved, err := service.identities.ResolveProjectMemberIDs(ctx, project.ID, nodeIDs)
	if err != nil {
		return SyncResult{}, err
	}
	result = SyncResult{SyncedAt: syncedAt, Exported: 0}
	present := make([]string, 0, len(remote))
	position := 0
	for _, candidate := range remote {
		issue := candidate.Issue
		issue.IssuerHolarkID = resolved[position]
		position++
		issue.AssigneeHolarkIDs = append([]string(nil), resolved[position:position+len(candidate.AssigneeGitHubNodeIDs)]...)
		position += len(candidate.AssigneeGitHubNodeIDs)
		_, inserted, err := service.projection.StoreSynced(ctx, project.ID, issue)
		if err != nil {
			return SyncResult{}, err
		}
		if inserted {
			result.Imported++
		} else {
			result.Updated++
		}
		present = append(present, issue.SyncExternalID)
	}
	if _, err := service.projection.DeleteSyncedAbsent(ctx, project.ID, string(issues.IssueSyncProviderGitHub), present); err != nil {
		return SyncResult{}, err
	}
	result.Issues, err = service.projection.List(ctx, project.ID)
	if err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

// SyncOpen imports open issues and verifies cached open issues missing from the listing.
// Only a full Sync may delete records absent from a provider snapshot.
func (service *Service) SyncOpen(ctx context.Context, projectSlug string) (result SyncResult, resultErr error) {
	defer func() {
		if service.syncRecorder != nil {
			resultErr = errors.Join(resultErr, service.syncRecorder.RecordSync(context.WithoutCancel(ctx), projectSlug, "issues", resultErr))
		}
	}()
	project, ok := service.projects(projectSlug)
	if !ok {
		return result, ErrProjectNotFound
	}
	unlock := service.lockProjectSync(project.ID)
	defer unlock()
	transport, ok := service.transport.(OpenTransport)
	if !ok {
		return result, ErrIssueSourceUnavailable
	}
	existing, err := service.projection.List(ctx, project.ID)
	if err != nil {
		return result, err
	}
	result.SyncedAt = service.now().UTC()
	remote, err := transport.SnapshotOpen(ctx, project.ID, project.RepositoryURL, result.SyncedAt)
	if err != nil {
		return result, err
	}
	apply := func(candidate RemoteIssue) {
		issue, err := service.withResolvedIdentities(ctx, project.ID, candidate)
		if err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("issue %s: %w", candidate.Issue.SyncExternalID, err))
			return
		}
		_, inserted, err := service.projection.StoreSynced(ctx, project.ID, issue)
		if err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("issue %s: %w", issue.SyncExternalID, err))
			return
		}
		if inserted {
			result.Imported++
		} else {
			result.Updated++
		}
	}
	seen := make(map[string]bool, len(remote))
	for _, candidate := range remote {
		seen[candidate.Issue.SyncExternalID] = true
		apply(candidate)
	}
	for _, current := range existing {
		if current.SyncProvider != string(issues.IssueSyncProviderGitHub) || current.Status != issues.IssueOpen || seen[current.SyncExternalID] {
			continue
		}
		candidate, err := transport.Get(ctx, current, project.RepositoryURL, result.SyncedAt)
		if err == nil && (candidate.Issue.SyncExternalID != current.SyncExternalID || candidate.Issue.SyncProvider != current.SyncProvider) {
			err = fmt.Errorf("%w: GitHub returned a different issue", ErrIssueSourceFailed)
		}
		if err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("issue %s: %w", current.ID, err))
			continue
		}
		apply(candidate)
	}
	result.Issues, err = service.projection.List(ctx, project.ID)
	return result, errors.Join(resultErr, err)
}

func (service *Service) withResolvedIdentities(ctx context.Context, projectID string, remote RemoteIssue) (issues.Issue, error) {
	if err := service.observeMembers(ctx, projectID, remote); err != nil {
		return issues.Issue{}, err
	}
	nodeIDs := append([]string{remote.CreatorGitHubNodeID}, remote.AssigneeGitHubNodeIDs...)
	resolved, err := service.identities.ResolveProjectMemberIDs(ctx, projectID, nodeIDs)
	if err != nil {
		return issues.Issue{}, err
	}
	issue := remote.Issue
	issue.IssuerHolarkID = resolved[0]
	issue.AssigneeHolarkIDs = append([]string(nil), resolved[1:]...)
	return issue, nil
}

func projectionPending(operation MutationOperation, remote RemoteIssue, cause error) error {
	return &ProjectionPendingError{
		GitHubIssueURL: remote.Reference.URL, GitHubIssueNumber: remote.Reference.Number,
		Operation: operation, Err: cause,
	}
}

func projectionPendingForIssue(operation MutationOperation, issue issues.Issue, safeToRetry bool, cause error) error {
	var payload struct {
		GitHub struct {
			URL    string `json:"url"`
			Number int    `json:"number"`
		} `json:"github"`
	}
	_ = json.Unmarshal(issue.SyncData, &payload)
	return &ProjectionPendingError{
		GitHubIssueURL: payload.GitHub.URL, GitHubIssueNumber: payload.GitHub.Number,
		Operation: operation, SafeToRetry: safeToRetry, Err: cause,
	}
}

func (s *Service) observeMembers(ctx context.Context, repo string, remote RemoteIssue) error {
	observer, ok := s.identities.(interface {
		ObserveProjectMember(context.Context, string, githubidentity.SourceMember) (string, error)
	})
	if !ok {
		return nil
	}
	for _, member := range remote.Members {
		if _, err := observer.ObserveProjectMember(ctx, repo, member); err != nil {
			return err
		}
	}
	return nil
}
