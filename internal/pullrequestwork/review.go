package pullrequestwork

import "context"

func (s *Service) withReviewFreshness(ctx context.Context, w Work) Work {
	return s.withReviewsFreshness(ctx, []Work{w})[0]
}

// Freshness is response-only. Capture each PR's current input once for the
// response, including failed captures, so review history shares one snapshot.
func (s *Service) withReviewsFreshness(ctx context.Context, works []Work) []Work {
	currentByPR := make(map[string]*ReviewInput)
	for i := range works {
		w := &works[i]
		w.Freshness = nil
		if w.Kind != KindReview || w.Provenance == nil || s.reviewChanges == nil {
			continue
		}
		current, captured := currentByPR[w.PullRequestID]
		if !captured {
			if pr, ok := s.catalog.PullRequest(w.PullRequestID); ok {
				base := pr.DiffBaseCommit
				if base == "" {
					base = pr.BaseCommit
				}
				var err error
				current, err = s.reviewChanges.Capture(ctx, base, pr.HeadCommit)
				if err != nil {
					current = nil
				}
			}
			currentByPR[w.PullRequestID] = current
		}
		if current == nil {
			continue
		}
		generated := w.Provenance
		w.Freshness = &ReviewFreshness{Generated: generated, Current: current,
			Outdated: generated.PatchFingerprint != current.PatchFingerprint && generated.MessagesFingerprint != current.MessagesFingerprint}
	}
	return works
}
