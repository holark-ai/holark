package holons

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/sessions"
)

const MaxTitleCharacters = 80

var ErrNotFound = errors.New("holon not found")
var ErrManualTerminalNotFound = errors.New("manual terminal not found")
var ErrInvalid = errors.New("invalid holon")
var ErrNotResumable = errors.New("holon is not resumable")
var ErrNotCancellable = errors.New("holon is not cancellable")
var ErrTerminalLimit = errors.New("manual terminal limit reached")

type Workspace struct {
	Path, Branch, BaseCommit string
}
type PublishedWorkspace struct {
	UpstreamBranch, HeadCommit string
}

type workspaceOptionsRepository interface {
	InspectWorkspaceWithOptions(context.Context, string, InspectOptions) (WorkspaceInspection, error)
}

type workspaceArchiver interface {
	ArchiveWorkspace(context.Context, string, string) error
	ReopenWorkspace(context.Context, string, string) error
}

// Repository is the narrow repository-owned port used by Holon workflows.
type Repository interface {
	CreateWorkspace(context.Context, string, string) (Workspace, error)
	InspectWorkspace(context.Context, string) (WorkspaceInspection, error)
	PublishWorkspace(context.Context, string, Publish) (PublishedWorkspace, error)
	RemoveWorkspace(context.Context, string) error
}

func (s *Service) ApplyGeneratedIdentity(ctx context.Context, id, expectedTitle, title, slug string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	if s.repository == nil {
		return Holon{}, ErrInvalid
	}
	renamer, ok := s.repository.(interface {
		RenameWorkspace(context.Context, string, string, string) (string, error)
	})
	if !ok {
		return Holon{}, ErrInvalid
	}
	branch, err := renamer.RenameWorkspace(ctx, id, h.WorktreeBranch, slug)
	if err != nil {
		return Holon{}, err
	}
	h.WorktreeBranch = branch
	if h.Title == expectedTitle && strings.TrimSpace(title) != "" {
		h.Title = NormalizeTitle(title, h.Prompt)
	}
	if err = s.store.Update(ctx, h); err != nil {
		return Holon{}, err
	}
	return h, nil
}

type Service struct {
	phases                  applicationPhases
	rebaseAutoCloseEligible func(context.Context, Holon) (bool, error)
	publicationTargets      PublicationTargetReader
	store                   Store
	locks                   holonLocks
	now                     func() time.Time
	repository              Repository
}

func NewService(store Store) *Service { return &Service{store: store, now: time.Now} }
func NewServiceWithRepository(store Store, repository Repository) *Service {
	return &Service{store: store, repository: repository, now: time.Now}
}
func (s *Service) List(ctx context.Context) ([]Holon, error) {
	values, err := s.store.List(ctx)
	for i := range values {
		values[i] = s.ProjectHolon(ctx, values[i])
	}
	return values, err
}
func (s *Service) Get(ctx context.Context, id string) (Holon, error) {
	h, err := s.store.Get(ctx, id)
	if err == nil {
		h = s.ProjectHolon(ctx, h)
	}
	return h, err
}

func NormalizeTitle(title, prompt string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		for _, line := range strings.Split(prompt, "\n") {
			if title = strings.TrimSpace(line); title != "" {
				break
			}
		}
	}
	if title == "" {
		title = "Holon"
	}
	r := []rune(title)
	if len(r) > MaxTitleCharacters {
		title = string(r[:MaxTitleCharacters])
	}
	return title
}
func validKind(k Kind) bool {
	switch k {
	case KindNormal, KindIssue, KindPullReview, KindPullWorker, KindPRMetadata, KindRebase:
		return true
	}
	return false
}
func newID(prefix string) string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b)
}

// ValidateStartup limits terminal startup to user-created Holons and PR workers.
func (in Create) ValidateStartup() error {
	if in.StartupMode != "" && in.StartupMode != "agent" && in.StartupMode != "terminal" {
		return ErrInvalid
	}
	if in.StartupMode == "terminal" && ((in.Kind != "" && in.Kind != KindNormal && in.Kind != KindPullWorker) || in.AgentType != "") {
		return ErrInvalid
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in Create) (Holon, error) {
	if err := in.ValidateStartup(); err != nil {
		return Holon{}, err
	}
	if in.Kind == "" {
		in.Kind = KindNormal
	}
	in.BaseBranch = CanonicalBaseBranch(in.BaseBranch)
	if !validKind(in.Kind) || strings.TrimSpace(in.BaseCommit) == "" || !utf8.ValidString(in.Prompt) {
		return Holon{}, ErrInvalid
	}
	now := s.now().UTC()
	workSessionStartCommit := strings.TrimSpace(in.WorkSessionStartCommit)
	if workSessionStartCommit == "" {
		workSessionStartCommit = in.BaseCommit
	}
	h := Holon{ID: newID("holon_"), Title: NormalizeTitle(in.Title, in.Prompt), Prompt: in.Prompt, Kind: in.Kind, Status: StatusQueued, BaseBranch: in.BaseBranch, BaseCommit: in.BaseCommit, WorkSessionStartCommit: workSessionStartCommit, UpstreamBranch: in.UpstreamBranch, UpstreamHeadCommit: in.UpstreamHeadCommit, IssueID: in.IssueID, PullRequestID: in.PullRequestID, CreatedAt: now}
	if err := s.prepareWorkspace(ctx, &h, in.RebaseTargetCommit); err != nil {
		return Holon{}, err
	}
	if in.AgentType != "" {
		agentTitle := NormalizeTitle(in.AgentTitle, "Agent")
		h.AgentSessions = []AgentSession{{Model: in.Model, Permissions: in.Permissions, ID: newID("agent_"), HolonID: h.ID, AgentType: in.AgentType, Title: agentTitle, Prompt: in.Prompt, Status: string(StatusQueued), Activity: protocol.ActivityStarting, CreatedAt: now, UpdatedAt: now}}
	}
	if err := s.store.Create(ctx, h); err != nil {
		if s.repository != nil {
			_ = s.repository.RemoveWorkspace(ctx, h.ID)
		}
		return Holon{}, err
	}
	return h, nil
}

// FinalizeTerminalStartup records the outcome without involving agent aggregation.
func (s *Service) FinalizeTerminalStartup(ctx context.Context, id string, startupErr error) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	if (h.Kind != KindNormal && h.Kind != KindPullWorker) || len(h.AgentSessions) != 0 || (h.Status != StatusQueued && h.Status != StatusPreparing) {
		return Holon{}, ErrInvalid
	}
	now := s.now().UTC()
	if startupErr != nil {
		h.Status = StatusFailed
		h.Reason = startupErr.Error()
		h.FinishedAt = &now
	} else {
		h.Status = StatusRunning
		h.StartedAt = &now
	}
	if err = s.store.Update(ctx, h); err != nil {
		return Holon{}, err
	}
	return s.store.Get(ctx, id)
}

func (s *Service) InspectWorkspace(ctx context.Context, id string) (WorkspaceInspection, error) {
	return s.InspectWorkspaceWithOptions(ctx, id, InspectOptions{})
}

func (s *Service) InspectWorkspaceWithOptions(ctx context.Context, id string, options InspectOptions) (WorkspaceInspection, error) {
	if options.Contents && (strings.TrimSpace(options.Path) == "" || options.SummaryOnly) {
		return WorkspaceInspection{}, ErrInvalid
	}

	if s.repository == nil {
		return WorkspaceInspection{}, ErrInvalid
	}
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return WorkspaceInspection{}, err
	}
	var inspection WorkspaceInspection
	options.BaseBranch = h.BaseBranch
	options.BranchBaseCommit = h.BaseCommit
	options.WorkSessionStartCommit = h.WorkSessionStartCommit
	if repository, ok := s.repository.(workspaceOptionsRepository); ok {
		inspection, err = repository.InspectWorkspaceWithOptions(ctx, id, options)
	} else {
		inspection, err = s.repository.InspectWorkspace(ctx, id)
	}
	if err != nil {
		return WorkspaceInspection{}, err
	}
	inspection.BaseBranch = h.BaseBranch
	if inspection.Branch == "" {
		inspection.Branch = h.WorktreeBranch
	}
	if inspection.BaseCommit == "" {
		inspection.BaseCommit = h.BaseCommit
	}
	if inspection.Files == nil {
		inspection.Files = append([]WorkspaceChange(nil), inspection.Changes...)
	}
	for index := range inspection.Files {
		if inspection.Files[index].Diff == "" {
			inspection.Files[index].Diff = inspection.Files[index].Patch
		}
		inspection.Files[index].DiffTruncated = inspection.Files[index].DiffTruncated || inspection.Files[index].Truncated
	}
	if inspection.Files == nil {
		inspection.Files = []WorkspaceChange{}
	}
	inspection.HasChanges = len(inspection.Files) > 0
	inspection.Dirty = inspection.Dirty || !inspection.Clean
	return inspection, nil
}
func (s *Service) Publish(ctx context.Context, id string, in Publish) (Holon, error) {
	return s.PublishWithCommitter(ctx, id, in, nil)
}

// PublishWithCommitter saves a linked workflow's records under the Holon
// mutation lock. A failed commit retains the old checkpoint for a push retry.
func (s *Service) PublishWithCommitter(ctx context.Context, id string, in Publish, commit func(context.Context, Holon) error) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	if s.repository == nil || h.WorktreePath == "" || strings.TrimSpace(in.UpstreamBranch) == "" {
		return Holon{}, ErrInvalid
	}
	if in.RequireSynchronized {
		if h.RebaseAttempt.Active() {
			return Holon{}, ErrRebaseActive
		}
		repo, ok := s.repository.(synchronizationRepository)
		if !ok {
			return Holon{}, ErrInvalid
		}
		i, err := repo.InspectSynchronization(ctx, id, in.ExpectedRemoteHead, in.IgnoredPaths)
		if err != nil {
			return Holon{}, err
		}
		if i.Dirty || i.Rebasing || i.Branch != h.WorktreeBranch || !i.Incorporated {
			return Holon{}, ErrRebaseRequired
		}
	}
	published, err := s.repository.PublishWorkspace(ctx, id, in)
	if err != nil {
		return Holon{}, err
	}
	h.Published = true
	h.UpstreamBranch = published.UpstreamBranch
	h.UpstreamHeadCommit = published.HeadCommit
	h.SynchronizedTargetCommit = published.HeadCommit
	if commit == nil {
		commit = s.store.Update
	}
	if err = commit(ctx, h); err != nil {
		return Holon{}, err
	}
	return h, nil
}
func (s *Service) Rename(ctx context.Context, id, title string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, e := s.store.Get(ctx, id)
	if e != nil {
		return Holon{}, e
	}
	h.Title = NormalizeTitle(title, h.Prompt)
	e = s.store.Update(ctx, h)
	return h, e
}

// End records idempotent shutdown intent. Runtime coordinators deliver that
// intent to live resources before finalizing and archiving the workspace.
func (s *Service) End(ctx context.Context, id string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	if h.ArchivedAt != nil {
		return h, nil
	}
	if !h.EndRequested {
		h.EndRequested = true
		if err := s.store.Update(ctx, h); err != nil {
			return Holon{}, err
		}
	}
	if h.Status == StatusCancelling || IsTerminal(h.Status) {
		return h, nil
	}
	return s.store.SetStatus(ctx, id, StatusCancelling, s.now().UTC())
}

func (s *Service) Reopen(ctx context.Context, id string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	if h.ReadOnly || (h.ArchivedAt == nil && h.Kind != KindPullReview) || !IsTerminal(h.Status) || h.BaseBranch == "" || h.BaseCommit == "" || h.WorktreeBranch == "" || h.WorktreePath == "" {
		return Holon{}, ErrNotResumable
	}
	visible := 0
	for _, agent := range h.AgentSessions {
		if agent.ClosedAt == nil {
			visible++
			if !IsTerminal(Status(agent.Status)) {
				return Holon{}, ErrNotResumable
			}
		}
	}
	if h.ArchivedAt != nil {
		repository, ok := s.repository.(workspaceArchiver)
		if !ok {
			return Holon{}, ErrNotResumable
		}
		if err = repository.ReopenWorkspace(ctx, h.ID, h.WorktreeBranch); err != nil {
			return Holon{}, err
		}
	}
	if err = s.clearResumedRebaseClosures(ctx, h); err != nil {
		return Holon{}, err
	}
	if visible > 0 {
		h, err = s.store.SetStatus(ctx, id, StatusQueued, s.now().UTC())
		if err != nil {
			return Holon{}, err
		}
	} else if h.Kind == KindNormal || h.Kind == KindPullWorker {
		h.Status = StatusRunning
		h.Reason = ""
		h.ExitCode = nil
		h.FinishedAt = nil
		now := s.now().UTC()
		h.StartedAt = &now
	}
	h.ArchivedAt = nil
	h.EndRequested = false
	if err = s.store.Update(ctx, h); err != nil {
		return Holon{}, err
	}
	return s.store.Get(ctx, id)
}

// Resume prepares retained agents in an existing checkout. It remains an
// internal recovery primitive; the user-facing whole-holon action is Reopen,
// which reconstructs archived checkouts and also accepts retained review workspaces.
func (s *Service) Resume(ctx context.Context, id string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	if h.ReadOnly || h.ArchivedAt != nil || !IsTerminal(h.Status) || h.BaseBranch == "" || h.BaseCommit == "" || h.WorktreeBranch == "" || h.WorktreePath == "" {
		return Holon{}, ErrNotResumable
	}
	visible := 0
	for _, agent := range h.AgentSessions {
		if agent.ClosedAt == nil {
			visible++
			if !IsTerminal(Status(agent.Status)) {
				return Holon{}, ErrNotResumable
			}
		}
	}
	if visible == 0 {
		return Holon{}, ErrNotResumable
	}
	if err = s.clearResumedRebaseClosures(ctx, h); err != nil {
		return Holon{}, err
	}
	return s.store.SetStatus(ctx, id, StatusQueued, s.now().UTC())
}
func (s *Service) archiveLocked(ctx context.Context, h Holon) (Holon, error) {
	if h.ArchivedAt != nil || h.ReadOnly || h.WorktreePath == "" || h.WorktreeBranch == "" {
		return h, nil
	}
	if hasLiveResource(h) {
		return h, nil
	}
	if repository, ok := s.repository.(workspaceArchiver); ok && !h.ReadOnly && h.WorktreePath != "" && h.WorktreeBranch != "" {
		if err := repository.ArchiveWorkspace(ctx, h.ID, h.WorktreeBranch); err != nil {
			return Holon{}, err
		}
	} else {
		return h, nil
	}
	now := s.now().UTC()
	h.ArchivedAt = &now
	if err := s.store.Update(ctx, h); err != nil {
		return Holon{}, err
	}
	return s.store.Get(ctx, h.ID)
}
func hasLiveResource(h Holon) bool {
	for _, a := range h.AgentSessions {
		// Keep the checkout until successful Rebase cleanup closes its tab. A
		// normal exit also needs Git verification before it can be archived.
		if a.ClosedAt == nil && (!IsTerminal(Status(a.Status)) || h.RebaseAttempt.ClosurePending(a.ID) ||
			(h.RebaseAttempt.Active() && h.RebaseCompletionReady(a.ID))) {
			return true
		}
	}
	for _, terminal := range h.ManualTerminals {
		if terminal.ClosedAt == nil {
			return true
		}
	}
	for _, editor := range h.IDEs {
		if editor.DesiredOpen && editor.State != "suspended" {
			return true
		}
	}
	return false
}

func finiteWorkspaceKind(kind Kind) bool {
	switch kind {
	case KindPullReview, KindPullWorker, KindPRMetadata, KindRebase:
		return true
	default:
		return false
	}
}

func (s *Service) archiveFiniteLocked(ctx context.Context, h Holon) (Holon, error) {
	if !finiteWorkspaceKind(h.Kind) || !IsTerminal(h.Status) {
		return h, nil
	}
	return s.archiveLocked(ctx, h)
}

// ArchiveFinite re-evaluates deferred archival after an external resource,
// such as an IDE, has durably stopped.
func (s *Service) ArchiveFinite(ctx context.Context, id string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	return s.archiveFiniteLocked(ctx, h)
}

// FinalizeCancellation completes cancellation only after every resource has
// reached a durable closed or terminal state. Agent outcomes stay intact when
// ending a Holon whose agents have already stopped.
func (s *Service) FinalizeCancellation(ctx context.Context, id string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	return s.finalizeCancellationLocked(ctx, h)
}

func (s *Service) finalizeCancellationLocked(ctx context.Context, h Holon) (Holon, error) {
	if h.ArchivedAt != nil || hasLiveResource(h) {
		return h, nil
	}
	if IsTerminal(h.Status) {
		return s.archiveLocked(ctx, h)
	}
	if h.Status != StatusCancelling {
		return h, nil
	}
	now := s.now().UTC()
	h.Status = StatusCancelled
	h.Reason = ""
	h.ExitCode = nil
	h.FinishedAt = &now
	if err := s.store.Update(ctx, h); err != nil {
		return Holon{}, err
	}
	return s.archiveLocked(ctx, h)
}
func (s *Service) AddAgentSession(ctx context.Context, id, agentType, prompt string) (Holon, error) {
	h, _, err := s.ReserveAgentSession(ctx, id, agentType, prompt, "")
	return h, err
}

// AgentRequestID persists the retry identity in the existing agent primary key.
func AgentRequestID(holonID, requestID string) string {
	sum := sha256.Sum256([]byte(holonID + "\x00" + requestID))
	return "agent_" + hex.EncodeToString(sum[:16])
}

func (s *Service) AddAgentSessionOnce(ctx context.Context, id, agentType, prompt, requestID string) (AgentSession, error) {
	h, _, err := s.ReserveAgentSession(ctx, id, agentType, prompt, requestID)
	return h.AgentSession(AgentRequestID(id, requestID)), err
}

// ReserveAgentSession returns whether this caller created the record. Callers
// must separately serialize launch attempts so retries can recover unstarted records.
func (s *Service) ReserveAgentSession(ctx context.Context, id, agentType, prompt, requestID string, model ...string) (Holon, bool, error) {
	return s.ReserveAgentSessionWithSelection(ctx, id, agentType, prompt, requestID, AgentSelection{Model: selectedModel(model)})
}

func (s *Service) ReserveAgentSessionWithSelection(ctx context.Context, id, agentType, prompt, requestID string, selection AgentSelection) (Holon, bool, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, false, lockErr
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return Holon{}, false, err
	}
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, false, err
	}
	prompt = strings.TrimSpace(prompt)
	agentID := newID("agent_")
	if requestID != "" {
		if len(requestID) > 128 {
			return h, false, ErrInvalid
		}
		agentID = AgentRequestID(id, requestID)
		if existing := h.AgentSession(agentID); existing.ID != "" {
			if existing.AgentType != agentType || existing.Prompt != prompt {
				return h, false, ErrInvalid
			}
			return h, false, nil
		}
	}
	if h.ReadOnly || h.ArchivedAt != nil || h.BaseCommit == "" || h.WorktreeBranch == "" || h.WorktreePath == "" {
		return Holon{}, false, ErrInvalid
	}
	inputState := ""
	if prompt == "" {
		inputState = "task_complete"
	}
	now := s.now().UTC()
	h, err = s.store.AddAgentSession(ctx, id, AgentSession{Model: selection.Model, Permissions: selection.Permissions, ID: agentID, HolonID: id, AgentType: agentType, Title: "Agent", Prompt: prompt, Status: string(StatusQueued), Activity: protocol.ActivityStarting, InputState: inputState, CreatedAt: now, UpdatedAt: now})
	slog.DebugContext(ctx, "agent reservation completed", "holon_id", id, "agent_id", agentID, "created", err == nil)
	return h, err == nil, err
}
func (s *Service) RenameAgentSession(ctx context.Context, id, agentID, title string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	for _, a := range h.AgentSessions {
		if a.ID == agentID {
			if a.ClosedAt != nil {
				return Holon{}, ErrNotFound
			}
			a.Title = NormalizeTitle(title, a.Prompt)
			a.UpdatedAt = s.now().UTC()
			return s.store.UpdateAgentSession(ctx, id, a, false)
		}
	}
	return Holon{}, ErrNotFound
}
func (s *Service) SetAgentSessionStatus(ctx context.Context, id, agentID string, status Status, reason, resumeTarget string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	now := s.now().UTC()
	for _, a := range h.AgentSessions {
		if a.ID == agentID {
			if !validStatus(status) {
				return Holon{}, ErrInvalid
			}
			a.Status = string(status)
			if status == StatusQueued {
				a.Activity = protocol.ActivityStarting
			}
			if IsTerminal(status) {
				a.Activity = AgentActivity(a)
			}
			a.Reason = reason
			a.ResumeTarget = resumeTarget
			a.UpdatedAt = now
			if status == StatusRunning && a.StartedAt == nil {
				a.StartedAt = &now
			}
			if IsTerminal(status) {
				a.FinishedAt = &now
			}
			aggregate := h.Status != StatusCancelling
			updated, updateErr := s.store.UpdateAgentSession(ctx, id, a, aggregate)
			if updateErr == nil {
				slog.DebugContext(ctx, "agent status changed", "holon_id", id, "agent_id", agentID, "status", status)
			}
			if updateErr != nil || aggregate || !IsTerminal(status) {
				if updateErr == nil && aggregate {
					return s.archiveFiniteLocked(ctx, updated)
				}
				return updated, updateErr
			}
			return s.finalizeCancellationLocked(ctx, updated)
		}
	}
	return Holon{}, ErrNotFound
}

func (s *Service) UpdateAgentObservation(ctx context.Context, id, agentID, terminalID, resumeTarget, rolloutPath, inputState, observabilityStatus, observabilityMessage string, activity protocol.AgentActivity, contextTokens *int64, expectedTerminal ...string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	for _, a := range h.AgentSessions {
		if a.ID != agentID {
			continue
		}
		if len(expectedTerminal) > 0 && expectedTerminal[0] != "" && (a.TerminalID != expectedTerminal[0] || a.ClosedAt != nil || IsTerminal(Status(a.Status))) {
			return h, nil
		}
		if terminalID != "" {
			a.TerminalID = terminalID
			a.Activity = protocol.ActivityStarting
		}
		if resumeTarget != "" {
			if a.ResumeTarget != resumeTarget {
				// Context usage belongs to the conversation, not the terminal tab.
				a.ContextTokens = nil
			}
			a.ResumeTarget = resumeTarget
		}
		if rolloutPath != "" {
			a.RolloutPath = rolloutPath
		}
		if activity != "" {
			a.Activity = activity
		}
		// Session initialization can clear the harness input before any new turn.
		// Preserve a commit's saved pause until working activity resumes, even
		// when the harness then reports only activity because its input is None.
		if a.CommitStartHead != "" && a.InputState == string(protocol.InputUserRequired) {
			if activity == protocol.ActivityWorking && inputState == "" {
				inputState = string(protocol.InputNone)
			} else if activity != protocol.ActivityWorking && inputState == string(protocol.InputNone) {
				inputState = ""
			}
		}
		if inputState != "" {
			a.InputState = inputState
		}
		// Recovery can replay completed activity without a new input state.
		// Keep a commit that needs guidance waiting until the next turn starts.
		if a.CommitStartHead != "" && a.InputState == string(protocol.InputUserRequired) && (activity == protocol.ActivityCompleted || activity == protocol.ActivityIdle) {
			a.Activity = protocol.ActivityNeedsInput
		}
		if observabilityStatus == "degraded" || (a.ObservabilityStatus == "degraded" && observabilityStatus != "healthy") {
			a.Activity = protocol.ActivityUnknown
		}
		if observabilityStatus != "" {
			a.ObservabilityStatus = observabilityStatus
			a.ObservabilityMessage = observabilityMessage
			if a.Status == string(StatusRestoring) && observabilityStatus == "healthy" {
				a.Status = string(StatusRunning)
				a.Reason = ""
				if a.StartedAt == nil {
					now := s.now().UTC()
					a.StartedAt = &now
				}
			}
		}
		if contextTokens != nil {
			if a.ContextTokens != nil && *a.ContextTokens == *contextTokens && terminalID == "" && resumeTarget == "" && rolloutPath == "" && inputState == "" && observabilityStatus == "" && activity == "" {
				return h, nil
			}
			value := *contextTokens
			a.ContextTokens = &value
		}
		a.UpdatedAt = s.now().UTC()
		return s.store.UpdateAgentSession(ctx, id, a, true)
	}
	return Holon{}, ErrNotFound
}

func (s *Service) CompleteAgentSession(ctx context.Context, id, agentID, expectedTerminalID string, exitCode int) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	for _, a := range h.AgentSessions {
		if a.ID != agentID {
			continue
		}
		if expectedTerminalID == "" || a.TerminalID != expectedTerminalID {
			return h, nil
		}
		now := s.now().UTC()
		a.TerminalID = ""
		a.UpdatedAt = now
		outcome, apply := sessions.ResolveHarnessProcessExit(sessions.HarnessLifecycleStatus(a.Status), exitCode)
		if !apply {
			return s.store.UpdateAgentSession(ctx, id, a, false)
		}
		a.ExitCode = &exitCode
		a.FinishedAt = &now
		a.Status = string(outcome.Status)
		a.Activity = AgentActivity(a)
		a.Reason = outcome.Reason
		aggregate := !IsTerminal(h.Status) && h.Status != StatusCancelling
		updated, updateErr := s.store.UpdateAgentSession(ctx, id, a, aggregate)
		if updateErr != nil || h.Status != StatusCancelling {
			if updateErr == nil && h.Status != StatusCancelling {
				return s.archiveFiniteLocked(ctx, updated)
			}
			return updated, updateErr
		}
		return s.finalizeCancellationLocked(ctx, updated)
	}
	return Holon{}, ErrNotFound
}

type CommitPromptTransition uint8

const (
	CommitPromptClean CommitPromptTransition = iota
	CommitPromptPrepareCompletion
	CommitPromptComplete
	CommitPromptAutoComplete
	CommitPromptRearm
	CommitPromptPrepareDiscuss
	CommitPromptDiscuss
	CommitPromptSkip
	CommitPromptKeep
	CommitPromptClose
)

// TransitionAgentCommitPrompt owns the snapshot-keyed prompt rules. The bool
// reports whether the transition was accepted; preparation transitions do not
// persist state.
func (s *Service) TransitionAgentCommitPrompt(ctx context.Context, id, agentID string, transition CommitPromptTransition, hash string) (Holon, bool, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, false, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, false, err
	}
	for _, agent := range h.AgentSessions {
		if agent.ID != agentID {
			continue
		}
		// Once verified, late observations or banner actions must not erase the
		// durable close intent before runtime shutdown and tab closure finish.
		if agent.CommitClosePending() {
			return h, false, nil
		}
		if transition == CommitPromptClose {
			if agent.ClosedAt != nil || agent.CommitStartHead == "" {
				return h, false, ErrInvalid
			}
			agent.CommitPrompt = &CommitPrompt{State: "commit_close_pending"}
			agent.CommitPromptChangeHash = ""
			return s.updateCommitPrompt(ctx, id, agent, true)
		}
		// A live commit discussion must retain its marker even if a banner action
		// arrives with a newer workspace snapshot. Commit forks keep it until closed.
		if agent.CommitPromptChangeHash == "" && agent.ActiveCommitDiscussion() {
			return h, false, nil
		}
		if transition == CommitPromptClean {
			if agent.CommitPrompt == nil && agent.CommitPromptChangeHash == "" {
				return h, false, nil
			}
			agent.CommitPrompt, agent.CommitPromptChangeHash = nil, ""
			return s.updateCommitPrompt(ctx, id, agent, true)
		}
		if transition == CommitPromptRearm {
			if agent.CommitPrompt == nil || agent.CommitPrompt.State != "continued_for_now" {
				return h, false, nil
			}
			agent.CommitPrompt = nil
			return s.updateCommitPrompt(ctx, id, agent, true)
		}
		if hash == "" {
			return Holon{}, false, ErrInvalid
		}
		same := agent.CommitPromptChangeHash == hash
		visible := same && agent.CommitPrompt != nil && agent.CommitPrompt.State == "dirty_prompt_visible"
		switch transition {
		case CommitPromptPrepareCompletion:
			return h, !same || agent.CommitPrompt == nil, nil
		case CommitPromptPrepareDiscuss:
			return h, !same || visible, nil
		case CommitPromptComplete, CommitPromptAutoComplete:
			if same && agent.CommitPrompt != nil {
				return h, false, nil
			}
			state := "dirty_prompt_visible"
			if transition == CommitPromptAutoComplete {
				state = "continued_for_now"
			}
			agent.CommitPrompt = &CommitPrompt{State: state}
		case CommitPromptDiscuss, CommitPromptSkip, CommitPromptKeep:
			if same && !visible {
				return h, false, nil
			}
			state := "commit_discussion_started"
			if transition == CommitPromptSkip {
				state = "skipped_for_change"
			} else if transition == CommitPromptKeep {
				state = "continued_for_now"
			}
			agent.CommitPrompt = &CommitPrompt{State: state}
		default:
			return Holon{}, false, ErrInvalid
		}
		agent.CommitPromptChangeHash = hash
		return s.updateCommitPrompt(ctx, id, agent, true)
	}
	return Holon{}, false, ErrNotFound
}

func (s *Service) updateCommitPrompt(ctx context.Context, id string, agent AgentSession, accepted bool) (Holon, bool, error) {
	agent.UpdatedAt = s.now().UTC()
	h, err := s.store.UpdateAgentSession(ctx, id, agent, false)
	return h, accepted, err
}

func (s *Service) CancelAgentSession(ctx context.Context, id, agentID string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	for _, a := range h.AgentSessions {
		if a.ID == agentID {
			if a.ClosedAt != nil || (IsTerminal(Status(a.Status)) && Status(a.Status) != StatusRecoveryFailed) || Status(a.Status) == StatusCancelling {
				return Holon{}, ErrNotCancellable
			}
			a.Status = string(StatusCancelling)
			a.Activity = protocol.ActivityIdle
			a.UpdatedAt = s.now().UTC()
			return s.store.UpdateAgentSession(ctx, id, a, true)
		}
	}
	return Holon{}, ErrNotFound
}

func (s *Service) ResumeAgentSession(ctx context.Context, id, agentID, resumeTarget string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	if h.ReadOnly || h.ArchivedAt != nil || h.BaseCommit == "" || h.WorktreeBranch == "" || h.WorktreePath == "" {
		return Holon{}, ErrNotResumable
	}
	for _, a := range h.AgentSessions {
		if a.ID == agentID {
			if a.ClosedAt != nil || !IsTerminal(Status(a.Status)) {
				return Holon{}, ErrNotResumable
			}
			if err = s.clearResumedRebaseClosures(ctx, h, agentID); err != nil {
				return Holon{}, err
			}
			now := s.now().UTC()
			a.TerminalID = ""
			a.Status = string(StatusQueued)
			a.Activity = protocol.ActivityStarting
			a.Reason = ""
			a.ExitCode = nil
			if strings.TrimSpace(resumeTarget) != "" {
				a.ResumeTarget = resumeTarget
			}
			a.StartedAt = nil
			a.FinishedAt = nil
			a.UpdatedAt = now
			return s.store.UpdateAgentSession(ctx, id, a, true)
		}
	}
	return Holon{}, ErrNotFound
}
func validStatus(v Status) bool {
	switch v {
	case StatusNaming, StatusQueued, StatusPreparing, StatusRunning, StatusCancelling, StatusCancelled, StatusCompleted, StatusFailed, StatusLost, StatusExpired, StatusRestoring, StatusRecoveryFailed:
		return true
	}
	return false
}
func (s *Service) CloseAgentSession(ctx context.Context, id, agentID string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	for _, a := range h.AgentSessions {
		if a.ID == agentID {
			if a.ClosedAt != nil {
				return s.finalizeTabCloseLocked(ctx, h)
			}
			now := s.now().UTC()
			if !IsTerminal(Status(a.Status)) {
				a.Status = string(StatusCancelling)
				a.Activity = protocol.ActivityIdle
			}
			a.ClosedAt = &now
			a.UpdatedAt = now
			updated, err := s.store.UpdateAgentSession(ctx, id, a, true)
			if err != nil {
				return Holon{}, err
			}
			return s.archiveFiniteLocked(ctx, updated)
		}
	}
	return Holon{}, ErrAgentSessionNotFound
}
func (s *Service) AddManualTerminal(ctx context.Context, id, title, cwd, terminalID string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	if h.ReadOnly || h.ArchivedAt != nil || h.WorktreePath == "" {
		return Holon{}, ErrInvalid
	}
	n := 0
	for _, t := range h.ManualTerminals {
		if t.ClosedAt == nil {
			n++
		}
	}
	if n >= 100 {
		return Holon{}, ErrTerminalLimit
	}
	now := s.now().UTC()
	return s.store.AddManualTerminal(ctx, id, ManualTerminal{ID: newID("manual_"), HolonID: id, TerminalID: terminalID, Title: NormalizeTitle(title, "Shell "+strconv.Itoa(n+1)), CWD: cwd, CreatedAt: now, UpdatedAt: now})
}
func (s *Service) UpdateManualTerminal(ctx context.Context, id, terminalID, title string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, e := s.store.Get(ctx, id)
	if e != nil {
		return Holon{}, e
	}
	for _, t := range h.ManualTerminals {
		if t.ID == terminalID && t.ClosedAt == nil {
			t.Title = NormalizeTitle(title, t.Title)
			t.UpdatedAt = s.now().UTC()
			return s.store.UpdateManualTerminal(ctx, id, t)
		}
	}
	return Holon{}, ErrNotFound
}
func (s *Service) CloseManualTerminal(ctx context.Context, id, terminalID string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, e := s.store.Get(ctx, id)
	if e != nil {
		return Holon{}, e
	}
	for _, t := range h.ManualTerminals {
		if t.ID == terminalID {
			if t.ClosedAt != nil {
				return s.finalizeTabCloseLocked(ctx, h)
			}
			now := s.now().UTC()
			t.TerminalID = ""
			t.ClosedAt = &now
			t.UpdatedAt = now
			updated, err := s.store.UpdateManualTerminal(ctx, id, t)
			if err != nil {
				return updated, err
			}
			return s.finalizeTabCloseLocked(ctx, updated)
		}
	}
	return Holon{}, ErrManualTerminalNotFound
}

func (s *Service) finalizeTabCloseLocked(ctx context.Context, h Holon) (Holon, error) {
	// Agent status alone does not imply whole-Holon shutdown. Only retry
	// cancellation archival when End durably requested it.
	if h.EndRequested {
		return s.finalizeCancellationLocked(ctx, h)
	}
	return s.archiveFiniteLocked(ctx, h)
}

// SetManualTerminalBinding atomically replaces the ephemeral PTY identity on
// a durable open tab. An empty binding records restart cleanup without closing
// the user's tab.
func (s *Service) SetManualTerminalBinding(ctx context.Context, id, terminalID, binding string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, e := s.store.Get(ctx, id)
	if e != nil {
		return Holon{}, e
	}
	for _, t := range h.ManualTerminals {
		if t.ID == terminalID && t.ClosedAt == nil {
			t.TerminalID = binding
			t.UpdatedAt = s.now().UTC()
			return s.store.UpdateManualTerminal(ctx, id, t)
		}
	}
	return Holon{}, ErrNotFound
}
func (s *Service) PullRequestActivity(ctx context.Context, prID string) ([]Holon, error) {
	if strings.TrimSpace(prID) == "" {
		return nil, ErrInvalid
	}
	return s.store.ListByPullRequest(ctx, prID)
}
func (s *Service) ReorderTabs(ctx context.Context, id string, tabs []TabRef) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	return s.store.ReorderTabs(ctx, id, tabs)
}

// PrepareAgentRecovery preserves durable conversation and tab identity while
// fencing observations from the old terminal. Repeated startup recovery is safe.
func (s *Service) PrepareAgentRecovery(ctx context.Context, id, agentID string) (Holon, error) {
	return s.prepareAgentRecovery(ctx, id, agentID, false)
}

// PrepareAgentResumeRecovery lets an explicit retry retain a successful Rebase
// conversation whose earlier cleanup failed. Startup recovery keeps that cleanup.
func (s *Service) PrepareAgentResumeRecovery(ctx context.Context, id, agentID string) (Holon, error) {
	return s.prepareAgentRecovery(ctx, id, agentID, true)
}

func (s *Service) prepareAgentRecovery(ctx context.Context, id, agentID string, explicit bool) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	if h.ArchivedAt != nil || h.ReadOnly {
		return h, ErrNotResumable
	}
	for _, a := range h.AgentSessions {
		if a.ID != agentID {
			continue
		}
		if a.ClosedAt != nil || (IsTerminal(Status(a.Status)) && Status(a.Status) != StatusRecoveryFailed) || Status(a.Status) == StatusCancelling {
			return h, ErrNotResumable
		}
		if explicit {
			if err = s.clearResumedRebaseClosures(ctx, h, agentID); err != nil {
				return h, err
			}
		}
		a.Status = string(StatusRestoring)
		a.TerminalID = ""
		a.Reason = ""
		a.ExitCode = nil
		a.FinishedAt = nil
		a.ObservabilityStatus = ""
		a.ObservabilityMessage = ""
		a.UpdatedAt = s.now().UTC()
		return s.store.UpdateAgentSession(ctx, id, a, true)
	}
	return h, ErrNotFound
}

// SelectTab records a view preference, including for archived and read-only holons.
func (s *Service) SelectTab(ctx context.Context, id, tabID string) error {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(tabID) == "" {
		return ErrInvalid
	}
	for _, agent := range h.AgentSessions {
		if agent.ID == tabID && agent.ClosedAt == nil {
			return s.store.SetSelectedTab(ctx, id, tabID)
		}
	}
	for _, terminal := range h.ManualTerminals {
		if terminal.ID == tabID && terminal.ClosedAt == nil {
			return s.store.SetSelectedTab(ctx, id, tabID)
		}
	}
	for _, ide := range h.IDEs {
		if ide.ID == tabID && ide.DesiredOpen {
			return s.store.SetSelectedTab(ctx, id, tabID)
		}
	}
	return ErrInvalid
}

// prepareWorkspace is shared by direct creation and durable queued reservations.
func (s *Service) prepareWorkspace(ctx context.Context, h *Holon, target string) error {
	if s.repository != nil {
		workspace, err := s.repository.CreateWorkspace(ctx, h.ID, h.WorkSessionStartCommit)
		if err != nil {
			return err
		}
		if workspace.BaseCommit != h.WorkSessionStartCommit || workspace.Path == "" || workspace.Branch == "" {
			_ = s.repository.RemoveWorkspace(ctx, h.ID)
			return ErrInvalid
		}
		h.WorktreePath, h.WorktreeBranch = workspace.Path, workspace.Branch
	}
	if target != "" {
		repo, ok := s.repository.(synchronizationRepository)
		var inspection SynchronizationInspection
		var err error
		if !ok {
			err = ErrInvalid
		} else {
			inspection, err = repo.InspectSynchronization(ctx, h.ID, target, nil)
		}
		if err != nil || !inspection.clean(h.WorktreeBranch) || inspection.HeadCommit != h.WorkSessionStartCommit {
			if s.repository != nil {
				_ = s.repository.RemoveWorkspace(context.WithoutCancel(ctx), h.ID)
			}
			return fmt.Errorf("rebase preparation failed: expected source %s and local target %s: %w", h.WorkSessionStartCommit, target, errors.Join(ErrInvalid, err))
		}
	}
	return nil
}
