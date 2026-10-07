package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryError preserves the server's retry deadline alongside the CLI error.
type RetryError struct {
	RetryAt time.Time
	Err     error
}

type apiResponse struct {
	Body       []byte
	Header     http.Header
	StatusCode int
	Status     string
}

func (err *RetryError) Error() string     { return err.Err.Error() }
func (err *RetryError) Unwrap() error     { return err.Err }
func (err *RetryError) RateLimited() bool { return true }
func (err *RetryError) Uncertain() bool   { return false }
func (err *RetryError) RetryAfter() time.Duration {
	return max(0, time.Until(err.RetryAt))
}

// runReadWithInput expects gh api --include output, including on failed reads.
func (client *CLIClient) runReadWithInput(ctx context.Context, input []byte, arguments ...string) ([]byte, error) {
	response, err := client.runReadResponse(ctx, input, arguments...)
	return response.Body, err
}

func (client *CLIClient) runReadResponse(ctx context.Context, input []byte, arguments ...string) (apiResponse, error) {
	data, runErr := client.runWithInput(ctx, input, arguments...)
	reader := bufio.NewReader(bytes.NewReader(data))
	response, parseErr := http.ReadResponse(reader, nil)
	if parseErr != nil {
		if runErr != nil {
			return apiResponse{}, runErr
		}
		return apiResponse{}, &Error{Code: ErrorCodeSyncFailed, Err: fmt.Errorf("decode gh api response headers: %w", parseErr)}
	}
	result := apiResponse{Header: response.Header, StatusCode: response.StatusCode, Status: response.Status}
	if runErr == nil && response.StatusCode >= 400 {
		runErr = &Error{Code: ErrorCodeSyncFailed, Err: fmt.Errorf("GitHub returned HTTP %s", response.Status)}
	}
	if runErr != nil {
		if limited, deadline := rateLimit(response.StatusCode, response.Header); limited {
			return result, &RetryError{RetryAt: deadline, Err: runErr}
		}
		return result, runErr
	}
	// gh has already decoded the HTTP body, so do not apply Content-Length or
	// Transfer-Encoding from the printed headers to it a second time.
	result.Body, parseErr = io.ReadAll(reader)
	return result, parseErr
}

func rateLimit(status int, headers http.Header) (bool, time.Time) {
	if value := headers.Get("Retry-After"); value != "" {
		if deadline := retryAfterDeadline(value); !deadline.IsZero() {
			return true, deadline
		}
	}
	remaining, remainingErr := strconv.ParseInt(headers.Get("X-RateLimit-Remaining"), 10, 64)
	if status != http.StatusTooManyRequests && (remainingErr != nil || remaining != 0) {
		return false, time.Time{}
	}
	deadline := resetDeadline(headers)
	if deadline.IsZero() {
		deadline = time.Now()
	}
	return true, deadline
}

func retryDeadline(headers http.Header) time.Time {
	if value := headers.Get("Retry-After"); value != "" {
		if deadline := retryAfterDeadline(value); !deadline.IsZero() {
			return deadline
		}
	}
	return resetDeadline(headers)
}

func retryAfterDeadline(value string) time.Time {
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Now().Add(time.Duration(seconds) * time.Second)
	}
	if deadline, err := http.ParseTime(value); err == nil {
		return deadline
	}
	return time.Time{}
}

func resetDeadline(headers http.Header) time.Time {
	if seconds, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		return time.Unix(seconds, 0)
	}
	return time.Time{}
}

func graphQLRateLimitError(body []byte, headers http.Header) error {
	var envelope struct {
		Errors []struct {
			Message    string `json:"message"`
			Type       string `json:"type"`
			Extensions struct {
				Type string `json:"type"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	for _, graphErr := range envelope.Errors {
		if !strings.EqualFold(graphErr.Type, "RATE_LIMITED") && !strings.EqualFold(graphErr.Extensions.Type, "RATE_LIMITED") {
			continue
		}
		deadline := retryDeadline(headers)
		if deadline.IsZero() {
			deadline = time.Now()
		}
		message := strings.TrimSpace(graphErr.Message)
		if message == "" {
			message = "GitHub GraphQL rate limit exceeded"
		}
		return &RetryError{RetryAt: deadline, Err: &Error{Code: ErrorCodeSyncFailed, Err: errors.New(message)}}
	}
	return nil
}
