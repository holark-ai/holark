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
	"os/exec"
)

const ErrorCodeMutationUncertain ErrorCode = "github_mutation_uncertain"

type RejectionError struct {
	Status  int
	Message string
}

func (e *RejectionError) Error() string   { return e.Message }
func (e *RejectionError) Uncertain() bool { return false }

// RequestMutation performs exactly one request. Response headers distinguish a
// provider rejection from an uncertain transport failure and a confirmed write.
func (client *CLIClient) RequestMutation(ctx context.Context, method, endpoint string, input, output any) error {
	args := []string{"api", "--include", "--method", method, endpoint}
	var payload []byte
	var err error
	if input != nil {
		payload, err = json.Marshal(input)
		if err != nil {
			return err
		}
		args = append(args, "--input", "-")
	}
	data, runErr := client.runWithInput(ctx, payload, args...)
	response, parseErr := http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), nil)
	if parseErr != nil {
		if errors.Is(runErr, exec.ErrNotFound) {
			return runErr
		}
		if runErr == nil {
			return &Error{Code: ErrorCodeMutationAccepted, Err: fmt.Errorf("GitHub accepted mutation but response could not be read: %w", parseErr)}
		}
		return &Error{Code: ErrorCodeMutationUncertain, Err: runErr}
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	if limited, deadline := rateLimit(response.StatusCode, response.Header); limited {
		cause := runErr
		if cause == nil {
			cause = &Error{Code: ErrorCodeSyncFailed, Err: fmt.Errorf("GitHub returned %s", response.Status)}
		}
		return &RetryError{RetryAt: deadline, Err: cause}
	}
	if endpoint == "graphql" {
		if err := graphQLRateLimitError(body, response.Header); err != nil {
			return err
		}
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		var message struct {
			Message string          `json:"message"`
			Errors  json.RawMessage `json:"errors"`
		}
		_ = json.Unmarshal(body, &message)
		if message.Message == "" {
			message.Message = response.Status
		}
		if len(message.Errors) > 0 {
			message.Message += " " + string(message.Errors)
		}
		return &RejectionError{Status: response.StatusCode, Message: message.Message}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &Error{Code: ErrorCodeMutationUncertain, Err: fmt.Errorf("GitHub returned %s", response.Status)}
	}
	if readErr != nil {
		return &Error{Code: ErrorCodeMutationAccepted, Err: readErr}
	}
	if output != nil {
		if err = json.Unmarshal(body, output); err != nil {
			return &Error{Code: ErrorCodeMutationAccepted, Err: err}
		}
	}
	return nil
}
