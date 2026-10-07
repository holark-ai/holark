// Package pullrequestreviews owns reviews submitted by the user. Agent review
// sessions and actionable comment threads remain in their respective features.
package pullrequestreviews

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

type Event string

const (
	Comment Event = "comment"
	Approve Event = "approve"
)

type WriteOutcome string

const (
	WriteNotAttempted WriteOutcome = "not_attempted"
	WriteRejected     WriteOutcome = "rejected"
	WriteUnconfirmed  WriteOutcome = "unconfirmed"
	WriteConfirmed    WriteOutcome = "confirmed"
)

type Review struct {
	ID            string       `json:"id"`
	PullRequestID string       `json:"pull_request_id"`
	Event         Event        `json:"event"`
	Body          string       `json:"body"`
	HeadCommit    string       `json:"head_commit"`
	State         string       `json:"state"` // local, pending, published, failed
	WriteOutcome  WriteOutcome `json:"write_outcome,omitempty"`
	ExternalID    string       `json:"external_id,omitempty"`
	URL           string       `json:"url,omitempty"`
	Error         string       `json:"error,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
}

type Submission struct {
	ID         string `json:"id"`
	Event      Event  `json:"event"`
	Body       string `json:"body"`
	HeadCommit string `json:"head_commit"`
}

type Target struct {
	ID                string
	Status            string
	HeadCommit        string
	ComparisonCurrent bool
	Provider          string
	RepositoryURL     string
	Number            int
}

type TargetReader interface {
	GetReviewTarget(context.Context, string) (Target, error)
}

type Repository interface {
	Get(context.Context, string) (Review, error)
	List(context.Context, string) ([]Review, error)
	Save(context.Context, Review) error
}

type Receipt struct{ ID, URL string }

type Gateway interface {
	Find(context.Context, Target, Review) (Receipt, bool, error)
	Create(context.Context, Target, Review) (Receipt, WriteOutcome, error)
}

var (
	ErrNotFound            = errors.New("manual review not found")
	ErrPullRequestNotFound = errors.New("pull request not found")
	ErrInvalidSubmission   = errors.New("invalid review submission")
	ErrReadOnly            = errors.New("this pull request is read-only")
	ErrStaleHead           = errors.New("the pull request changed; review the latest changes before submitting")
)

type Service struct {
	mu      sync.Mutex
	reviews Repository
	targets TargetReader
	gateway Gateway
}

func New(reviews Repository, targets TargetReader, gateway Gateway) *Service {
	return &Service{reviews: reviews, targets: targets, gateway: gateway}
}

func (s *Service) List(ctx context.Context, id string) ([]Review, error) {
	if _, err := s.targets.GetReviewTarget(ctx, id); err != nil {
		return nil, err
	}
	return s.reviews.List(ctx, id)
}

// The caller keeps one ID per submission so a lost response can be retried.
// Persist intent before contacting the provider; retries recover its receipt
// before checking freshness. Only a write known not to have happened can be sent
// again; a missing receipt is not evidence that an interrupted write failed.
func (s *Service) Submit(ctx context.Context, id string, input Submission) (Review, error) {
	input.Body = strings.TrimSpace(input.Body)
	if !validID(input.ID) || input.HeadCommit == "" || len(input.HeadCommit) > 64 || len(utf16.Encode([]rune(input.Body))) > 4000 ||
		(input.Event != Comment && input.Event != Approve) || (input.Event == Comment && input.Body == "") {
		return Review{}, ErrInvalidSubmission
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.reviews.Get(ctx, input.ID)
	if err == nil {
		if previous.PullRequestID != id || previous.Event != input.Event || previous.Body != input.Body || previous.HeadCommit != input.HeadCommit {
			return Review{}, ErrInvalidSubmission
		}
		if previous.State == "local" || previous.State == "published" {
			return previous, nil
		}
	} else if !errors.Is(err, ErrNotFound) {
		return Review{}, err
	}
	target, err := s.targets.GetReviewTarget(ctx, id)
	if err != nil {
		return Review{}, err
	}
	if previous.ID != "" {
		return s.publish(ctx, target, previous)
	}
	if err := canSubmit(target, input.HeadCommit); err != nil {
		return Review{}, err
	}
	review := Review{ID: input.ID, PullRequestID: id, Event: input.Event, Body: input.Body,
		HeadCommit: input.HeadCommit, State: "local", WriteOutcome: WriteNotAttempted, CreatedAt: time.Now().UTC()}
	if target.Provider != "" {
		review.State = "pending"
	}
	if err := s.reviews.Save(ctx, review); err != nil {
		return Review{}, err
	}
	if review.State == "local" {
		return review, nil
	}
	return s.publish(ctx, target, review)
}

func (s *Service) Retry(ctx context.Context, pullRequestID, id string) (Review, error) {
	review, err := s.reviews.Get(ctx, id)
	if err != nil {
		return Review{}, err
	}
	if review.PullRequestID != pullRequestID {
		return Review{}, ErrNotFound
	}
	return s.Submit(ctx, pullRequestID, Submission{ID: review.ID, Event: review.Event, Body: review.Body, HeadCommit: review.HeadCommit})
}

func (s *Service) publish(ctx context.Context, target Target, review Review) (Review, error) {
	// Older submissions did not record their write outcome. Recover them
	// conservatively because their POST may already have reached GitHub.
	if review.WriteOutcome == "" {
		review.WriteOutcome = WriteUnconfirmed
	}
	var receipt Receipt
	var found bool
	var err error
	if target.Provider != "github" || target.RepositoryURL == "" || target.Number <= 0 || s.gateway == nil {
		err = errors.New("GitHub review publication is unavailable for this pull request")
	} else {
		receipt, found, err = s.gateway.Find(ctx, target, review)
		if err == nil && !found {
			if review.WriteOutcome != WriteNotAttempted && review.WriteOutcome != WriteRejected {
				err = errors.New("GitHub did not confirm the review; retry to check its status")
			} else if err = canSubmit(target, review.HeadCommit); err == nil {
				// Save before sending so a crash, cancellation, or failed result
				// checkpoint leaves this submission available for receipt recovery.
				review.State, review.Error, review.WriteOutcome = "pending", "", WriteUnconfirmed
				if err := s.reviews.Save(ctx, review); err != nil {
					return Review{}, err
				}
				receipt, review.WriteOutcome, err = s.gateway.Create(ctx, target, review)
			}
		}
	}
	if err == nil && receipt.ID == "" {
		err = errors.New("GitHub did not confirm the review; retry to check its status")
	}
	if err != nil {
		review.State, review.Error = "failed", err.Error()
		if review.WriteOutcome != WriteNotAttempted && review.WriteOutcome != WriteRejected {
			review.State = "pending"
		}
	} else {
		review.State, review.Error, review.ExternalID, review.URL = "published", "", receipt.ID, receipt.URL
		review.WriteOutcome = WriteConfirmed
	}
	if err := s.reviews.Save(context.WithoutCancel(ctx), review); err != nil {
		return Review{}, err
	}
	return review, nil
}

func canSubmit(target Target, head string) error {
	if target.Status != "wip" && target.Status != "draft" && target.Status != "open" {
		return ErrReadOnly
	}
	if !target.ComparisonCurrent || target.HeadCommit == "" || target.HeadCommit != head {
		return ErrStaleHead
	}
	return nil
}

func validID(id string) bool {
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
			return false
		}
	}
	return true
}
