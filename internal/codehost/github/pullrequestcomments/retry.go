package pullrequestcomments

import (
	"errors"
	"strings"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
)

func providerRetryDelay(err error) time.Duration {
	var retry interface{ RetryAfter() time.Duration }
	if errors.As(err, &retry) {
		return retry.RetryAfter()
	}
	return 0
}

// Read errors carry refresh backoff without changing publication error handling.
func refreshProviderError(err error) error {
	delay := providerRetryDelay(err)
	lower := strings.ToLower(err.Error())
	var rejection *githubapi.RejectionError
	if strings.Contains(lower, "rate limit") || strings.Contains(lower, "http 429") ||
		(errors.As(err, &rejection) && (rejection.Status == 429 || rejection.Status == 403)) {
		return pullrequestcomments.RateLimitedProviderError(err, delay)
	}
	if delay > 0 {
		return pullrequestcomments.RetryableProviderError(err, delay)
	}
	return providerError(err)
}
