package pullrequestcomments

import (
	"errors"
	"testing"
	"time"
)

func TestPublicationBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{{1, time.Minute}, {2, 2 * time.Minute}, {6, 32 * time.Minute}, {7, time.Hour}, {20, time.Hour}}
	for _, test := range tests {
		if got := publicationBackoff(test.attempt); got != test.want {
			t.Errorf("attempt %d backoff = %v, want %v", test.attempt, got, test.want)
		}
	}
}

func TestProviderErrorClassification(t *testing.T) {
	retryable, retryAfter := classifyProviderError(RetryableProviderError(errors.New("rate limited"), 17*time.Second))
	if !retryable || retryAfter != 17*time.Second {
		t.Fatalf("retry classification = %v, %v", retryable, retryAfter)
	}
	retryable, _ = classifyProviderError(PermanentProviderError(errors.New("invalid")))
	if retryable {
		t.Fatal("permanent provider error was retryable")
	}
}
