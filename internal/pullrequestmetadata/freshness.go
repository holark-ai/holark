package pullrequestmetadata

import (
	"context"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"time"
)

// Provenance belongs to saved text, independently of later generation attempts.
type Provenance struct {
	DiffBaseCommit      string `json:"diff_base_commit"`
	HeadCommit          string `json:"head_commit"`
	PatchFingerprint    string `json:"patch_fingerprint"`
	MessagesFingerprint string `json:"messages_fingerprint"`
}

type Changes interface {
	Capture(context.Context, string, string) (*Provenance, error)
}

type Freshness struct {
	Status    string      `json:"status"`
	Error     string      `json:"error,omitempty"`
	Generated *Provenance `json:"generated"`
	Current   *Provenance `json:"current"`
	Outdated  bool        `json:"outdated"`
}

type ApplicationFailure struct {
	Operation string `json:"operation"`
	Message   string `json:"message"`
}

type GeneratedOutput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// State is persisted with the description. Pending output survives application
// failures so retrying a save does not need another agent turn.
type State struct {
	OpeningOperationID string              `json:"opening_operation_id,omitempty"`
	ProviderUpdatedAt  time.Time           `json:"provider_updated_at,omitempty"`
	Provenance         *Provenance         `json:"provenance,omitempty"`
	Attempt            *Provenance         `json:"attempt,omitempty"`
	Output             *GeneratedOutput    `json:"output,omitempty"`
	Imported           bool                `json:"imported,omitempty"`
	GenerationError    string              `json:"generation_error,omitempty"`
	ApplicationError   *ApplicationFailure `json:"application_error,omitempty"`
}

func diffBase(snapshot Snapshot) string {
	if snapshot.DiffBaseCommit != "" {
		return snapshot.DiffBaseCommit
	}
	return snapshot.BaseCommit
}

func (service *Service) freshness(ctx context.Context, snapshot Snapshot) *Freshness {
	generated := snapshot.State.Provenance
	if generated == nil || service.changes == nil {
		return nil
	}
	var current *Provenance
	var err error
	if snapshot.ComparisonState != "" && snapshot.ComparisonState != pullrequestlifecycle.ComparisonReady {
		err = pullrequestlifecycle.ErrComparisonUnavailable
	} else {
		current, err = service.changes.Capture(ctx, diffBase(snapshot), snapshot.HeadCommit)
	}
	if err != nil {
		result := &Freshness{Status: "unknown", Generated: generated, Error: "Description freshness could not be checked."}
		if value, ok := service.lastFreshness.Load(snapshot.ID); ok {
			previous := value.(*Freshness)
			if previous.Generated != nil && *previous.Generated == *generated {
				result.Outdated = previous.Outdated
				result.Current = previous.Current
			}
		}
		return result
	}
	result := &Freshness{Status: "current", Generated: generated, Current: current,
		Outdated: generated.PatchFingerprint != current.PatchFingerprint && generated.MessagesFingerprint != current.MessagesFingerprint}
	if result.Outdated {
		result.Status = "outdated"
	}
	service.lastFreshness.Store(snapshot.ID, result)
	return result
}
