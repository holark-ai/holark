package pullrequestmetadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

const (
	MaxTitleCharacters       = 160
	MaxDescriptionCharacters = 4000
	MaxInstructionCharacters = 4000
	MaxArtifactBytes         = 96 * 1024
	MetadataArtifactPath     = ".holark/pr-metadata.json"
)

var (
	ErrInvalidRequest          = errors.New("invalid pull request metadata request")
	ErrPullRequestNotFound     = errors.New("pull request not found")
	ErrPullRequestInactive     = errors.New("pull request is inactive")
	ErrUnsupportedProvider     = errors.New("pull request provider is unsupported")
	ErrProviderUnavailable     = errors.New("pull request provider is unavailable")
	ErrProviderFailed          = errors.New("pull request provider update failed")
	ErrApplicationFailed       = errors.New("pull request metadata application failed")
	ErrUpdateFailed            = errors.New("pull request metadata update failed")
	ErrAgentTurnActive         = errors.New("pull request metadata agent turn is active")
	ErrAgentSessionUnavailable = errors.New("pull request metadata agent session is unavailable")
	ErrAgentUnavailable        = errors.New("pull request metadata agent is unavailable")
	ErrArtifactInvalid         = errors.New("pull request metadata artifact is invalid")
)

type AgentAction string

const (
	ActionImprove    AgentAction = "improve"
	ActionRegenerate AgentAction = "regenerate"
)

type PreparationTarget string

const (
	PreparationTargetWIP   PreparationTarget = "wip"
	PreparationTargetDraft PreparationTarget = "draft"
	PreparationTargetOpen  PreparationTarget = "open"
)

func (target PreparationTarget) Valid() bool {
	return target == PreparationTargetWIP || target == PreparationTargetDraft || target == PreparationTargetOpen
}

type Snapshot struct {
	ComparisonState    pullrequestlifecycle.ComparisonState
	State              State
	ID                 string
	RepositoryID       string
	RepositoryURL      string
	Title              string
	Description        string
	Status             string
	BaseBranch         string
	BaseCommit         string
	HeadBranch         string
	HeadCommit         string
	DiffBaseCommit     string
	SyncProvider       string
	SyncExternalID     string
	MetadataSessionID  string
	PreparationTarget  PreparationTarget
	PreparedHeadCommit string
	UpdatedAt          time.Time
}

type AgentSession struct {
	Input      *Provenance `json:"-"`
	ID         string      `json:"id"`
	RuntimeID  string      `json:"runtime_id"`
	Status     string      `json:"status"`
	InputState string      `json:"input_state"`
	Error      string      `json:"error"`
}

type Metadata struct {
	HeadCommit         string              `json:"head_commit"`
	DiffBaseCommit     string              `json:"diff_base_commit"`
	ViewRevision       int64               `json:"view_revision"`
	Freshness          *Freshness          `json:"freshness,omitempty"`
	GenerationError    string              `json:"generation_error,omitempty"`
	ApplicationError   *ApplicationFailure `json:"application_error,omitempty"`
	GenerationComplete bool                `json:"generation_complete"`
	PullRequestID      string              `json:"pull_request_id"`
	Title              string              `json:"title"`
	Description        string              `json:"description"`
	UpdatedAt          time.Time           `json:"updated_at"`
	AgentSession       *AgentSession       `json:"agent_session,omitempty"`
	PreparationTarget  PreparationTarget   `json:"preparation_target,omitempty"`
}

type Request struct {
	Title       *string
	Description *string
}

type TerminalSize struct {
	Columns int
	Rows    int
}

type AgentRequest struct {
	Action      AgentAction
	Instruction string
	RuntimeID   string
	Terminal    TerminalSize
}

type StartAgentSessionRequest struct {
	PullRequestID string
	RepositoryID  string
	RepositoryURL string
	BaseBranch    string
	BaseCommit    string
	HeadBranch    string
	HeadCommit    string
	RuntimeID     string
	Terminal      TerminalSize
	Prompt        string
	ArtifactPath  string
}

type ResumeAgentSessionRequest struct {
	PullRequestID string
	SessionID     string
	RepositoryID  string
	RepositoryURL string
	BaseBranch    string
	RuntimeID     string
	Terminal      TerminalSize
	Prompt        string
	ArtifactPath  string
}

type Artifact struct {
	Path               string
	Data               []byte
	OwnerPullRequestID string
}

type Store interface {
	Get(context.Context, string) (Snapshot, error)
	GetByMetadataSession(context.Context, string) (Snapshot, error)
	Update(context.Context, string, string, string, string, time.Time, State) error
	SaveState(context.Context, string, State) error
	SetMetadataSession(context.Context, string, string) error
	ClearMetadataSession(context.Context, string, string) (bool, error)
	InitializePreparation(context.Context, string, PreparationTarget) error
	MarkPrepared(context.Context, string, string) error
	ClearPreparation(context.Context, string) error
}

type Lifecycle interface {
	Transition(context.Context, string, PreparationTarget) error
}

type LifecycleFunc func(context.Context, string, PreparationTarget) error

func (fn LifecycleFunc) Transition(ctx context.Context, id string, target PreparationTarget) error {
	return fn(ctx, id, target)
}

type Provider interface {
	Update(context.Context, Snapshot, string, string) (time.Time, error)
}

type Projection interface {
	pullrequestlifecycle.ActionRegistry
	GetPullRequest(string) (pullrequestlifecycle.PullRequest, bool)
}

type AgentSessions interface {
	Start(context.Context, StartAgentSessionRequest) (AgentSession, error)
	Resume(context.Context, ResumeAgentSessionRequest) (AgentSession, error)
	Inspect(context.Context, string) (AgentSession, error)
	ConsumeArtifact(context.Context, string) (Artifact, error)
	SetTurnError(context.Context, string, string) error
	ClearTurnError(context.Context, string) error
	Retire(context.Context, string) error
}

type Clock interface{ Now() time.Time }

type CommitMetadata interface {
	SingleCommit(context.Context, string, string) (*GeneratedOutput, error)
}

type Options struct {
	CommitMetadata CommitMetadata
	Changes        Changes
	Provider       Provider
	Projection     Projection
	Clock          Clock
	AgentSessions  AgentSessions
	Lifecycle      Lifecycle
	Templates      prompttemplates.Reader
}

type Service struct {
	lastFreshness   sync.Map
	artifactMu      sync.Mutex
	artifactImports int
	artifactsDone   chan struct{}
	commitMetadata  CommitMetadata
	changes         Changes
	locks           sync.Map
	store           Store
	provider        Provider
	projection      Projection
	clock           Clock
	agentSessions   AgentSessions
	lifecycle       Lifecycle
	templates       prompttemplates.Reader
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func New(store Store, options Options) *Service {
	clock := options.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Service{
		commitMetadata: options.CommitMetadata,
		store:          store, changes: options.Changes, provider: options.Provider, projection: options.Projection, clock: clock,
		agentSessions: options.AgentSessions, lifecycle: options.Lifecycle, templates: options.Templates,
	}
}

func (service *Service) Get(ctx context.Context, id string) (Metadata, error) {
	if service == nil || service.store == nil {
		return Metadata{}, ErrInvalidRequest
	}
	unlock := service.lock(id)
	defer unlock()
	snapshot, err := service.store.Get(ctx, id)
	if err != nil {
		return Metadata{}, err
	}
	return service.metadata(ctx, snapshot), nil
}

// RequestOpen checks persisted metadata and initializes and launches preparation
// under the same lock used by manual generation and artifact application.
func (service *Service) RequestOpen(ctx context.Context, id, operationID string) (bool, error) {
	if service == nil || service.store == nil || id == "" {
		return false, ErrInvalidRequest
	}
	if err := service.waitForArtifactImports(ctx); err != nil {
		return false, err
	}
	unlock := service.lock(id)
	defer unlock()
	snapshot, err := service.store.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if snapshot.Status != "wip" && snapshot.Status != "draft" {
		// Artifact application may have completed opening while this request waited.
		if snapshot.Status == "open" {
			return true, nil
		}
		return false, ErrPullRequestInactive
	}
	if activePreparation(snapshot) {
		if snapshot.State.ApplicationError != nil {
			snapshot.State.OpeningOperationID = operationID
			if err := service.store.SaveState(ctx, id, snapshot.State); err != nil {
				return true, err
			}
			return true, service.applyOutput(ctx, &snapshot)
		}
		return true, ErrAgentTurnActive
	}
	if service.agentBusy(ctx, snapshot) {
		return false, ErrAgentTurnActive
	}
	if snapshot.State.ApplicationError == nil && strings.TrimSpace(snapshot.Description) != "" {
		return false, nil
	}
	snapshot.State.OpeningOperationID = operationID
	if err := service.store.SaveState(ctx, id, snapshot.State); err != nil {
		return false, err
	}
	if err := service.store.InitializePreparation(ctx, id, PreparationTargetOpen); err != nil {
		return false, err
	}
	snapshot.PreparationTarget, snapshot.PreparedHeadCommit = PreparationTargetOpen, ""
	if snapshot.State.ApplicationError != nil {
		return true, service.applyOutput(ctx, &snapshot)
	}
	_, err = service.startPreparation(ctx, snapshot)
	return true, err
}

func (service *Service) ClearPreparation(ctx context.Context, id string) error {
	if service == nil || service.store == nil || id == "" {
		return ErrInvalidRequest
	}
	unlock := service.lock(id)
	defer unlock()
	return service.store.ClearPreparation(ctx, id)
}

// PublicationBlocked reads the durable opening request without the generation
// lock, so pages can project progress while an artifact is being applied.
func (service *Service) PublicationBlocked(ctx context.Context, id string) (bool, error) {
	snapshot, err := service.store.Get(ctx, id)
	if err != nil {
		return true, err
	}
	return activePreparation(snapshot), nil
}

func (service *Service) Update(ctx context.Context, id string, request Request) (Metadata, error) {
	if service == nil || service.store == nil || (request.Title == nil && request.Description == nil) {
		return Metadata{}, ErrInvalidRequest
	}
	unlock := service.lock(id)
	defer unlock()
	snapshot, err := service.store.Get(ctx, id)
	if err != nil {
		return Metadata{}, err
	}
	if !active(snapshot.Status) {
		return Metadata{}, ErrPullRequestInactive
	}
	if service.agentBusy(ctx, snapshot) {
		return Metadata{}, ErrAgentTurnActive
	}
	if request.Description != nil {
		snapshot.State.Provenance = nil
		// A manual edit supersedes a pending generated save as well.
		if snapshot.State.ApplicationError != nil && snapshot.State.ApplicationError.Operation == "save" {
			snapshot.State.Output, snapshot.State.ApplicationError = nil, nil
			snapshot.State.Imported = true
		}
	}
	if err := service.updateSnapshot(ctx, &snapshot, request); err != nil {
		return Metadata{}, err
	}
	if request.Description != nil {
		if err := service.store.ClearPreparation(ctx, id); err != nil {
			return Metadata{}, err
		}
		snapshot.PreparationTarget, snapshot.PreparedHeadCommit = "", ""
	}
	return service.metadata(ctx, snapshot), nil
}

// startPreparation reuses a lone commit's message when it includes a description.
// The caller holds the service lock.
func (service *Service) startPreparation(ctx context.Context, snapshot Snapshot) (Metadata, error) {
	id := snapshot.ID
	if !activePreparation(snapshot) {
		return Metadata{}, ErrInvalidRequest
	}
	if service.agentBusy(ctx, snapshot) {
		return Metadata{}, ErrAgentTurnActive
	}
	if service.commitMetadata != nil && snapshot.MetadataSessionID == "" && !snapshot.State.Imported {
		output, err := service.commitMetadata.SingleCommit(ctx, diffBase(snapshot), snapshot.HeadCommit)
		if err != nil {
			return Metadata{}, err
		}
		if output != nil && strings.TrimSpace(output.Description) != "" {
			if service.changes != nil {
				snapshot.State.Attempt, err = service.changes.Capture(ctx, diffBase(snapshot), snapshot.HeadCommit)
				if err != nil {
					return Metadata{}, err
				}
			}
			snapshot.State.Output = output
			snapshot.State.GenerationError, snapshot.State.ApplicationError = "", nil
			if err := service.store.SaveState(ctx, id, snapshot.State); err != nil {
				return Metadata{}, err
			}
			if err := service.applyOutput(ctx, &snapshot); err != nil {
				return Metadata{}, err
			}
			latest, err := service.store.Get(ctx, id)
			if err != nil {
				return Metadata{}, err
			}
			return service.metadata(ctx, latest), nil
		}
	}
	return service.startAgent(ctx, id, AgentRequest{Action: ActionRegenerate})
}

func (service *Service) StartAgent(ctx context.Context, id string, request AgentRequest) (Metadata, error) {
	if service == nil || service.store == nil || service.agentSessions == nil {
		return Metadata{}, ErrInvalidRequest
	}
	unlock := service.lock(id)
	defer unlock()
	return service.startAgent(ctx, id, request)
}

func (service *Service) startAgent(ctx context.Context, id string, request AgentRequest) (Metadata, error) {
	if service.agentSessions == nil {
		return Metadata{}, ErrInvalidRequest
	}
	if (request.Action != ActionImprove && request.Action != ActionRegenerate) || utf8.RuneCountInString(request.Instruction) > MaxInstructionCharacters {
		return Metadata{}, ErrInvalidRequest
	}
	snapshot, err := service.store.Get(ctx, id)
	if err != nil {
		return Metadata{}, err
	}
	if !active(snapshot.Status) {
		return Metadata{}, ErrPullRequestInactive
	}
	if service.agentBusy(ctx, snapshot) {
		return Metadata{}, ErrAgentTurnActive
	}

	if request.Action == ActionImprove && strings.TrimSpace(snapshot.Title) == "" && strings.TrimSpace(snapshot.Description) == "" {
		return Metadata{}, ErrInvalidRequest
	}
	definition, _ := prompttemplates.DefinitionByKey(prompttemplates.PullRequestMetadataKey)
	template := definition.DefaultValue
	if service.templates != nil {
		template = service.templates.Read(ctx, prompttemplates.PullRequestMetadataKey)
	}
	prompt := agentPrompt(template, diffBase(snapshot))

	var session AgentSession
	if request.Action == ActionImprove && snapshot.MetadataSessionID != "" {
		state, inspectErr := service.agentSessions.Inspect(ctx, snapshot.MetadataSessionID)
		if inspectErr != nil || !sessionAvailable(state) {
			return Metadata{}, ErrAgentSessionUnavailable
		}
		session, err = service.agentSessions.Resume(ctx, ResumeAgentSessionRequest{
			SessionID: state.ID, PullRequestID: snapshot.ID, RepositoryID: snapshot.RepositoryID, RepositoryURL: snapshot.RepositoryURL, BaseBranch: snapshot.BaseBranch,
			RuntimeID: state.RuntimeID, Terminal: request.Terminal, Prompt: prompt, ArtifactPath: MetadataArtifactPath,
		})
	} else {
		if snapshot.MetadataSessionID != "" {
			_ = service.agentSessions.Retire(ctx, snapshot.MetadataSessionID)
		}
		baseCommit := snapshot.DiffBaseCommit
		if baseCommit == "" {
			baseCommit = snapshot.BaseCommit
		}
		session, err = service.agentSessions.Start(ctx, StartAgentSessionRequest{
			PullRequestID: snapshot.ID, RepositoryID: snapshot.RepositoryID, RepositoryURL: snapshot.RepositoryURL, BaseBranch: snapshot.BaseBranch, BaseCommit: baseCommit,
			HeadBranch: snapshot.HeadBranch, HeadCommit: snapshot.HeadCommit, RuntimeID: request.RuntimeID, Terminal: request.Terminal, Prompt: prompt, ArtifactPath: MetadataArtifactPath,
		})
		if err == nil {
			err = service.store.SetMetadataSession(ctx, snapshot.ID, session.ID)
		}
		if err == nil {
			snapshot.MetadataSessionID = session.ID
		}
	}
	if err != nil {
		snapshot.State.GenerationError = "Metadata generation could not start: " + err.Error()
		return Metadata{}, errors.Join(ErrAgentUnavailable, err, service.store.SaveState(ctx, snapshot.ID, snapshot.State))
	}
	input := session.Input
	if input == nil && service.changes != nil {
		input, err = service.changes.Capture(ctx, diffBase(snapshot), snapshot.HeadCommit)
		if err != nil {
			return Metadata{}, errors.Join(ErrUpdateFailed, err)
		}
	}
	snapshot.State.Attempt, snapshot.State.Output, snapshot.State.Imported = input, nil, false
	snapshot.State.GenerationError, snapshot.State.ApplicationError = "", nil
	if err := service.store.SaveState(ctx, snapshot.ID, snapshot.State); err != nil {
		return Metadata{}, err
	}
	return service.metadata(ctx, snapshot), nil
}

// RetireInactive retires the linked metadata workspace after the pull request
// becomes closed or merged. The compare-and-clear preserves a newer link if a
// concurrent operation replaced the session.
func (service *Service) RetireInactive(ctx context.Context, id string) error {
	if service == nil || service.store == nil || service.agentSessions == nil || id == "" {
		return ErrInvalidRequest
	}
	unlock := service.lock(id)
	defer unlock()
	snapshot, err := service.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if active(snapshot.Status) || snapshot.MetadataSessionID == "" {
		return nil
	}
	if err := service.agentSessions.Retire(ctx, snapshot.MetadataSessionID); err != nil {
		return errors.Join(ErrAgentUnavailable, err)
	}
	_, err = service.store.ClearMetadataSession(ctx, snapshot.ID, snapshot.MetadataSessionID)
	return err
}

func (service *Service) ImportArtifact(ctx context.Context, sessionID string) error {
	if service == nil || service.store == nil || service.agentSessions == nil || sessionID == "" {
		return ErrInvalidRequest
	}
	finishImport := service.beginArtifactImport()
	defer finishImport()
	initial, err := service.store.GetByMetadataSession(ctx, sessionID)
	if err != nil {
		return err
	}
	unlock := service.lock(initial.ID)
	defer unlock()
	snapshot, err := service.store.GetByMetadataSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if snapshot.State.Imported || snapshot.State.ApplicationError != nil {
		return nil
	}
	if snapshot.State.Output != nil {
		return service.applyOutput(ctx, &snapshot)
	}
	artifact, err := service.agentSessions.ConsumeArtifact(ctx, sessionID)
	if err != nil {
		return service.importFailure(ctx, sessionID, "Metadata output was not produced.", err)
	}
	if artifact.Path != MetadataArtifactPath {
		return service.importFailure(ctx, sessionID, "Metadata output was written to the wrong path.", ErrArtifactInvalid)
	}
	if artifact.OwnerPullRequestID != "" && artifact.OwnerPullRequestID != snapshot.ID {
		return service.importFailure(ctx, sessionID, "Metadata output belongs to a different pull request.", ErrArtifactInvalid)
	}
	if len(artifact.Data) > MaxArtifactBytes {
		return service.importFailure(ctx, sessionID, "Metadata output is too large.", ErrArtifactInvalid)
	}
	if !utf8.Valid(artifact.Data) {
		return service.importFailure(ctx, sessionID, "Metadata output is not valid UTF-8.", ErrArtifactInvalid)
	}
	var output struct {
		Title       *string `json:"title"`
		Description *string `json:"description"`
	}
	decoder := json.NewDecoder(bytes.NewReader(artifact.Data))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&output); decodeErr != nil || decoder.Decode(&struct{}{}) != io.EOF || output.Title == nil || output.Description == nil {
		return service.importFailure(ctx, sessionID, "Metadata output is not valid JSON with exactly title and description.", ErrArtifactInvalid)
	}
	title, description := strings.TrimSpace(*output.Title), strings.TrimSpace(*output.Description)
	if title == "" || utf8.RuneCountInString(title) > MaxTitleCharacters || utf8.RuneCountInString(description) > MaxDescriptionCharacters {
		return service.importFailure(ctx, sessionID, "Metadata output has an invalid title or description length.", ErrArtifactInvalid)
	}
	snapshot.State.Output = &GeneratedOutput{Title: title, Description: description}
	if err := service.store.SaveState(ctx, snapshot.ID, snapshot.State); err != nil {
		return errors.Join(ErrApplicationFailed, err)
	}
	return service.applyOutput(ctx, &snapshot)
}

// RetryApplication reuses durable output or the already saved description.
func (service *Service) RetryApplication(ctx context.Context, id string) (Metadata, error) {
	unlock := service.lock(id)
	defer unlock()
	snapshot, err := service.store.Get(ctx, id)
	if err != nil {
		return Metadata{}, err
	}
	if !active(snapshot.Status) {
		return Metadata{}, ErrPullRequestInactive
	}
	if snapshot.State.ApplicationError == nil {
		return service.metadata(ctx, snapshot), nil
	}
	if err := service.applyOutput(ctx, &snapshot); err != nil {
		return Metadata{}, err
	}
	latest, err := service.store.Get(ctx, id)
	if err != nil {
		return Metadata{}, err
	}
	return service.metadata(ctx, latest), nil
}

func (service *Service) applicationFailure(ctx context.Context, snapshot *Snapshot, operation, message string, cause error) error {
	snapshot.State.ApplicationError = &ApplicationFailure{Operation: operation, Message: message}
	return errors.Join(ErrApplicationFailed, cause, service.store.SaveState(ctx, snapshot.ID, snapshot.State))
}

func (service *Service) applyOutput(ctx context.Context, snapshot *Snapshot) error {
	if output := snapshot.State.Output; output != nil {
		previous := snapshot.State.Provenance
		snapshot.State.Provenance = snapshot.State.Attempt
		if err := service.updateSnapshot(ctx, snapshot, Request{Title: &output.Title, Description: &output.Description}); err != nil {
			// A projection failure can occur after the atomic database save.
			if saved, readErr := service.store.Get(ctx, snapshot.ID); readErr == nil {
				snapshot.State.Provenance = saved.State.Provenance
			} else {
				snapshot.State.Provenance = previous
			}
			return service.applicationFailure(ctx, snapshot, "save", "Generated metadata could not be saved. Retry saving.", err)
		}
		head := snapshot.HeadCommit
		if snapshot.State.Attempt != nil {
			head = snapshot.State.Attempt.HeadCommit
		}
		if err := service.store.MarkPrepared(ctx, snapshot.ID, head); err != nil {
			return service.applicationFailure(ctx, snapshot, "save", "Generated metadata could not be marked ready. Retry saving.", err)
		}
		snapshot.State.Output, snapshot.State.Imported = nil, true
	}
	latest, err := service.store.Get(ctx, snapshot.ID)
	if err != nil {
		return service.applicationFailure(ctx, snapshot, "save", "Generated metadata was saved, but preparation state could not be read. Retry saving.", err)
	}
	if latest.Status == "open" && latest.PreparationTarget == PreparationTargetOpen {
		if err := service.store.ClearPreparation(ctx, snapshot.ID); err != nil {
			return service.applicationFailure(ctx, snapshot, "save", "Generated metadata was saved, but preparation could not be cleared. Retry saving.", err)
		}
		latest.PreparationTarget, latest.PreparedHeadCommit = "", ""
	}
	publish := activePreparation(latest)
	snapshot.State.ApplicationError = nil
	if publish {
		// Persist the pending operation with completion before crossing the
		// publication boundary, so an interrupted process still offers retry.
		snapshot.State.ApplicationError = &ApplicationFailure{Operation: "publication", Message: "Publication did not finish. Retry publication."}
	}
	if err := service.store.SaveState(ctx, snapshot.ID, snapshot.State); err != nil {
		return service.applicationFailure(ctx, snapshot, "save", "Generated metadata could not be marked ready. Retry saving.", err)
	}
	// Output is durable; artifact cleanup cannot invalidate the description.
	if service.agentSessions != nil && snapshot.MetadataSessionID != "" {
		_ = service.agentSessions.ClearTurnError(ctx, snapshot.MetadataSessionID)
	}
	if !publish {
		return nil
	}
	if service.lifecycle == nil {
		return service.applicationFailure(ctx, snapshot, "publication", "Generated metadata was saved, but the pull request could not be created. Retry publication.", ErrUpdateFailed)
	}
	openingID := latest.State.OpeningOperationID
	operation, found, err := service.projection.GetOperation(ctx, openingID)
	if err != nil {
		return err
	}
	if found && operation.Status == "failed" {
		openingID = pullrequestlifecycle.RequestID(ctx) + ":opening"
		snapshot.State.OpeningOperationID = openingID
		if err := service.store.SaveState(ctx, snapshot.ID, snapshot.State); err != nil {
			return err
		}
	}
	if err := service.lifecycle.Transition(pullrequestlifecycle.WithRequestID(ctx, openingID), latest.ID, latest.PreparationTarget); err != nil {
		return service.applicationFailure(ctx, snapshot, "publication", "Generated metadata was saved, but the pull request could not be created. Retry publication.", err)
	}
	if err := service.store.ClearPreparation(ctx, snapshot.ID); err != nil {
		return errors.Join(ErrApplicationFailed, err)
	}
	snapshot.State.ApplicationError = nil
	if err := service.store.SaveState(ctx, snapshot.ID, snapshot.State); err != nil {
		return errors.Join(ErrApplicationFailed, err)
	}
	return nil
}

func (service *Service) importFailure(ctx context.Context, sessionID, message string, cause error) error {
	if err := service.agentSessions.SetTurnError(ctx, sessionID, message); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (service *Service) updateSnapshot(ctx context.Context, snapshot *Snapshot, request Request) (resultErr error) {
	title, description := snapshot.Title, snapshot.Description
	if request.Title != nil {
		title = strings.TrimSpace(*request.Title)
	}
	if request.Description != nil {
		description = strings.TrimSpace(*request.Description)
	}
	if title == "" || utf8.RuneCountInString(title) > MaxTitleCharacters || utf8.RuneCountInString(description) > MaxDescriptionCharacters {
		return ErrInvalidRequest
	}
	operation, created, err := service.projection.BeginOperation(ctx, pullrequestlifecycle.Operation{
		RequestID: pullrequestlifecycle.RequestID(ctx) + ":metadata", PullRequestID: snapshot.ID, Kind: "metadata",
		Groups: []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.MetadataGroup},
		Steps:  map[string]pullrequestlifecycle.OperationStep{"metadata": {Status: "running", Title: title, Summary: description, HeadCommit: snapshot.HeadCommit}},
	})
	if err != nil {
		return err
	}
	if !created {
		return pullrequestlifecycle.ErrOperationInProgress
	}
	defer func() {
		if resultErr == nil {
			return // The metadata transaction completed the operation.
		}
		outcome := "failed"
		var uncertain interface{ Uncertain() bool }
		if errors.As(resultErr, &uncertain) && uncertain.Uncertain() {
			outcome = "uncertain"
		}
		_, err := service.projection.CompleteOperation(context.WithoutCancel(ctx), operation.RequestID, outcome, resultErr.Error())
		resultErr = errors.Join(resultErr, err)
	}()
	if snapshot.SyncProvider != "" && (title != snapshot.Title || description != snapshot.Description) {
		if snapshot.SyncProvider != "github" || service.provider == nil {
			return ErrUnsupportedProvider
		}
		observedAt, err := service.provider.Update(ctx, *snapshot, title, description)
		if err != nil {
			return err
		}
		snapshot.State.ProviderUpdatedAt = observedAt
	}
	updatedAt := service.clock.Now().UTC()
	if err := service.store.Update(ctx, snapshot.ID, operation.RequestID, title, description, updatedAt, snapshot.State); err != nil {
		return err
	}
	snapshot.Title, snapshot.Description, snapshot.UpdatedAt = title, description, updatedAt
	return nil
}

func (service *Service) metadata(ctx context.Context, snapshot Snapshot) Metadata {
	result := Metadata{
		HeadCommit: snapshot.HeadCommit, DiffBaseCommit: diffBase(snapshot),
		PullRequestID: snapshot.ID, Title: snapshot.Title, Description: snapshot.Description,
		UpdatedAt: snapshot.UpdatedAt, Freshness: service.freshness(ctx, snapshot),
		GenerationError: snapshot.State.GenerationError, ApplicationError: snapshot.State.ApplicationError, GenerationComplete: snapshot.State.Imported,
	}
	if pr, found := service.projection.GetPullRequest(snapshot.ID); found && pr.HeadCommit == snapshot.HeadCommit && pr.Title == snapshot.Title && pr.Summary == snapshot.Description && pr.DiffBaseCommit == snapshot.DiffBaseCommit {
		result.ViewRevision = pr.ViewRevision
	}
	if activePreparation(snapshot) {
		result.PreparationTarget = snapshot.PreparationTarget
	}
	if snapshot.MetadataSessionID != "" && service.agentSessions != nil {
		session, err := service.agentSessions.Inspect(ctx, snapshot.MetadataSessionID)
		if err == nil {
			result.AgentSession = &session
		} else {
			result.AgentSession = &AgentSession{ID: snapshot.MetadataSessionID, Status: "lost", Error: "Metadata agent session is unavailable. Regenerate to recover."}
		}
	}
	return result
}

func (service *Service) agentBusy(ctx context.Context, snapshot Snapshot) bool {
	if snapshot.MetadataSessionID == "" || service.agentSessions == nil {
		return false
	}
	session, err := service.agentSessions.Inspect(ctx, snapshot.MetadataSessionID)
	if err != nil {
		return false
	}
	return sessionBusy(session)
}

func sessionBusy(session AgentSession) bool {
	return session.Status == "queued" || session.Status == "preparing" || session.Status == "running"
}

func sessionAvailable(session AgentSession) bool {
	return session.Status == "completed" || session.Status == "expired"
}

func agentPrompt(template, baseCommit string) string {
	return prompttemplates.Render(template, map[string]string{
		"base_commit":                baseCommit,
		"artifact_path":              MetadataArtifactPath,
		"max_title_characters":       fmt.Sprint(MaxTitleCharacters),
		"max_description_characters": fmt.Sprint(MaxDescriptionCharacters),
	})
}

func active(status string) bool {
	return status == "wip" || status == "draft" || status == "open"
}

func activePreparation(snapshot Snapshot) bool {
	return (snapshot.Status == "wip" || snapshot.Status == "draft") && snapshot.PreparationTarget == PreparationTargetOpen
}

// Serialize metadata execution for one PR without blocking other PRs during Git or provider work.
func (service *Service) lock(id string) func() {
	value, _ := service.locks.LoadOrStore(id, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// Opening waits for already-running artifact applications, preserving publication
// ordering without holding a repository-wide mutex during agent or provider work.
func (service *Service) beginArtifactImport() func() {
	service.artifactMu.Lock()
	if service.artifactImports == 0 {
		service.artifactsDone = make(chan struct{})
	}
	service.artifactImports++
	service.artifactMu.Unlock()
	return func() {
		service.artifactMu.Lock()
		service.artifactImports--
		if service.artifactImports == 0 {
			close(service.artifactsDone)
		}
		service.artifactMu.Unlock()
	}
}
func (service *Service) waitForArtifactImports(ctx context.Context) error {
	service.artifactMu.Lock()
	if service.artifactImports == 0 {
		service.artifactMu.Unlock()
		return nil
	}
	done := service.artifactsDone
	service.artifactMu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}
