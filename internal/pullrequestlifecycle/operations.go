package pullrequestlifecycle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/holark-ai/holark/internal/repository"
	"time"
)

type FieldGroup string

const (
	LifecycleGroup FieldGroup = "lifecycle"
	TopologyGroup  FieldGroup = "topology"
	MetadataGroup  FieldGroup = "metadata"
)

// ActiveOperation is a view of durable pending intent, never a stored PR field.
type ActiveOperation struct {
	RequestID       string       `json:"request_id"`
	Kind            string       `json:"kind"`
	RequestedStatus Status       `json:"requested_status,omitempty"`
	Groups          []FieldGroup `json:"groups"`
	Status          string       `json:"status"`
	MutationStarted bool         `json:"mutation_started,omitempty"`
}

func (o ActiveOperation) Active() bool { return o.Status == "running" || o.Status == "uncertain" }

type CreationInputs struct {
	BaseRef        repository.BranchIdentity `json:"base_ref"`
	HeadRef        repository.BranchIdentity `json:"head_ref"`
	BranchRead     *repository.BranchRead    `json:"branch_read,omitempty"`
	RepositoryID   string                    `json:"repository_id"`
	Title          string                    `json:"title"`
	Summary        string                    `json:"summary"`
	BaseBranch     string                    `json:"base_branch"`
	BaseCommit     string                    `json:"base_commit"`
	HeadBranch     string                    `json:"head_branch"`
	DiffBaseCommit string                    `json:"diff_base_commit"`
}
type OperationStep struct {
	Creation   *CreationInputs `json:"creation,omitempty"`
	Title      string          `json:"title,omitempty"`
	Summary    string          `json:"summary,omitempty"`
	Status     string          `json:"status"`
	HeadCommit string          `json:"head_commit,omitempty"`
	ExternalID string          `json:"external_id,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// MutationInputs captures the accepted cache version before ownership is claimed.
// It lives in the existing operation document; no separate cache is needed.
type MutationInputs struct {
	BaseRef                repository.BranchIdentity `json:"base_ref"`
	HeadRef                repository.BranchIdentity `json:"head_ref"`
	BaseGeneration         int64                     `json:"base_generation"`
	HeadGeneration         int64                     `json:"head_generation"`
	BaseMutationEpoch      int64                     `json:"base_mutation_epoch"`
	HeadMutationEpoch      int64                     `json:"head_mutation_epoch"`
	RelationshipGeneration int64                     `json:"relationship_generation"`
	ComparisonState        ComparisonState           `json:"comparison_state"`
	RepositoryID           string                    `json:"repository_id"`
	SyncProvider           string                    `json:"sync_provider"`
	SyncExternalID         string                    `json:"sync_external_id"`
	Status                 Status                    `json:"status"`
	LifecycleGeneration    int64                     `json:"lifecycle_generation"`
	TopologyGeneration     int64                     `json:"topology_generation"`
	BaseBranch             string                    `json:"base_branch"`
	HeadBranch             string                    `json:"head_branch"`
	BaseCommit             string                    `json:"base_commit"`
	HeadCommit             string                    `json:"head_commit"`
	DiffBaseCommit         string                    `json:"diff_base_commit"`
	HeadRepositoryURL      string                    `json:"head_repository_url"`
	BaseRepositoryURL      string                    `json:"base_repository_url"`
}

func CaptureMutationInputs(p PullRequest) *MutationInputs {
	source := DecodeGitHubObservation(p.SyncData)
	inputs := &MutationInputs{BaseRef: p.BaseRef, HeadRef: p.HeadRef, RelationshipGeneration: p.RelationshipGeneration, ComparisonState: p.ComparisonState, RepositoryID: p.RepositoryID, SyncProvider: p.SyncProvider, SyncExternalID: p.SyncExternalID, Status: p.Status,
		LifecycleGeneration: p.LifecycleGeneration, TopologyGeneration: p.TopologyGeneration,
		BaseBranch: p.BaseBranch, HeadBranch: p.HeadBranch, BaseCommit: p.BaseCommit, HeadCommit: p.HeadCommit, DiffBaseCommit: p.DiffBaseCommit,
		HeadRepositoryURL: repository.RepositoryIdentity(source.HeadRepositoryURL), BaseRepositoryURL: repository.RepositoryIdentity(source.BaseRepositoryURL)}
	if p.Comparison != nil {
		inputs.BaseGeneration = p.Comparison.Inputs.Base.Generation
		inputs.HeadGeneration = p.Comparison.Inputs.Head.Generation
		inputs.BaseMutationEpoch = p.Comparison.Inputs.Base.MutationEpoch
		inputs.HeadMutationEpoch = p.Comparison.Inputs.Head.MutationEpoch
	}
	return inputs
}

var ErrSynchronizationStale = errors.New("pull request changed or is protected; synchronize again")

type Synchronizer interface {
	SyncPullRequest(context.Context, string) error
}

type Operation struct {
	BranchInputs    *ComparisonInputs        `json:"branch_inputs,omitempty"`
	ExpectedInputs  *MutationInputs          `json:"expected_inputs,omitempty"`
	OwnerID         string                   `json:"owner_id,omitempty"`
	RequestID       string                   `json:"request_id"`
	PullRequestID   string                   `json:"pull_request_id,omitempty"`
	HolonID         string                   `json:"holon_id,omitempty"`
	Kind            string                   `json:"kind"`
	RequestedStatus Status                   `json:"requested_status,omitempty"`
	Groups          []FieldGroup             `json:"groups"`
	Status          string                   `json:"status"`
	ExpectedHead    string                   `json:"expected_head,omitempty"`
	Steps           map[string]OperationStep `json:"steps,omitempty"`
	Error           string                   `json:"error,omitempty"`
	CreatedAt       time.Time                `json:"created_at"`
	UpdatedAt       time.Time                `json:"updated_at"`
}

func (o Operation) Active() bool { return o.Status == "running" || o.Status == "uncertain" }

var ErrOperationInProgress = errors.New("a conflicting pull request action is already in progress")

type ActionRegistry interface {
	RecordOperationStep(context.Context, string, string, string, OperationStep) error
	BeginOperation(context.Context, Operation) (Operation, bool, error)
	CompleteOperation(context.Context, string, string, string) (Operation, error)
	GetOperation(context.Context, string) (Operation, bool, error)
	ListUnfinishedOperations(context.Context) ([]Operation, error)
}
type ObservationVersion struct {
	Lifecycle int64
	Topology  int64
	Metadata  int64
}
type ObservationToken struct {
	Sequence int64
	Versions map[string]ObservationVersion
	Groups   []FieldGroup
}
type ObservationRegistry interface {
	BeginObservation(context.Context, string, []FieldGroup) (ObservationToken, error)
	UpsertObservedPullRequests(string, []PullRequest, ObservationToken) (int, int, error)
}
type Confirmation struct {
	Status     Status `json:"status,omitempty"`
	HeadCommit string `json:"head_commit,omitempty"`
}
type requestIDKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	if id != "" {
		return id
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// MatchesOperationRequest compares stable intent, allowing creation to acquire
// its PR identity after initial registration against a Holon.
func MatchesOperationRequest(existing, requested Operation) bool {
	if existing.Kind != requested.Kind || existing.RequestedStatus != requested.RequestedStatus || existing.HolonID != requested.HolonID {
		return false
	}
	return existing.PullRequestID == requested.PullRequestID || requested.Kind == "create" && requested.PullRequestID == ""
}
func OperationRequestConflict() error {
	return Error{Code: "request_id_conflict", Message: "This request ID was already used for a different pull request action."}
}
func StoredOperationError(operation Operation) error {
	message := operation.Error
	if message == "" {
		message = "The pull request action failed."
	}
	return Error{Code: "pull_request_action_failed", Message: message}
}
