package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/holark-ai/holark/internal/protocol"
)

const (
	MaxRecordBytes = 32 * 1024
	MaxSpoolBytes  = 8 * 1024 * 1024
)

type Fact struct {
	Type          string `json:"type"`
	SessionID     string `json:"session_id,omitempty"`
	ParentID      string `json:"parent_id,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	MessageID     string `json:"message_id,omitempty"`
	Status        string `json:"status,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	ContextTokens *int64 `json:"context_tokens,omitempty"`
}

func DecodeFact(data []byte) (Fact, bool, error) {
	if len(data) == 0 || len(data) > MaxRecordBytes {
		return Fact{}, false, errors.New("OpenCode fact exceeds its record bound")
	}
	var header struct {
		Type string `json:"type"`
	}
	if err := decodeJSON(data, &header, false); err != nil {
		return Fact{}, false, fmt.Errorf("decode OpenCode fact type: %w", err)
	}
	if header.Type == "" {
		return Fact{}, false, errors.New("OpenCode fact type is missing")
	}
	known := header.Type == "observer_initialized" || header.Type == "observer_failed" || header.Type == "session_selected" || header.Type == "session_error" || header.Type == "session_created" || header.Type == "session_status" ||
		header.Type == "background_task_started" || header.Type == "background_task_settled" ||
		header.Type == "context_usage" ||
		header.Type == "question_opened" || header.Type == "question_closed" ||
		header.Type == "permission_opened" || header.Type == "permission_closed"
	if !known {
		return Fact{Type: header.Type}, false, nil
	}
	var fact Fact
	if err := decodeJSON(data, &fact, true); err != nil {
		return Fact{}, false, fmt.Errorf("decode known OpenCode fact: %w", err)
	}
	if err := validateFact(fact); err != nil {
		return Fact{}, false, err
	}
	return fact, true, nil
}

func decodeJSON(data []byte, target any, disallowUnknown bool) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if disallowUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("OpenCode fact has trailing JSON")
	}
	return nil
}

func validateFact(fact Fact) error {
	validID := func(value string) bool { return protocol.ValidIdentifier(value) }
	noExtra := func(session, parent, request, message, status, outcome, contextTokens bool) bool {
		return (session || fact.SessionID == "") && (parent || fact.ParentID == "") &&
			(request || fact.RequestID == "") && (message || fact.MessageID == "") && (status || fact.Status == "") &&
			(outcome || fact.Outcome == "") && (contextTokens || fact.ContextTokens == nil)
	}
	switch fact.Type {
	case "observer_initialized":
		if (fact.Status == "" || fact.Status == "terminal") && noExtra(false, false, false, false, true, false, false) {
			return nil
		}
	case "observer_failed":
		if noExtra(false, false, false, false, false, false, false) {
			return nil
		}
	case "session_selected":
		if validID(fact.SessionID) && noExtra(true, false, false, false, false, false, false) {
			return nil
		}
	case "session_created":
		if validID(fact.SessionID) && (fact.ParentID == "" || validID(fact.ParentID)) && noExtra(true, true, false, false, false, false, false) {
			return nil
		}
	case "session_error":
		if validID(fact.SessionID) && (fact.Outcome == "error" || fact.Outcome == "interrupted") && noExtra(true, false, false, false, false, true, false) {
			return nil
		}
	case "session_status":
		if validID(fact.SessionID) && (fact.Status == "busy" || fact.Status == "retry" || fact.Status == "idle") && noExtra(true, false, false, false, true, false, false) {
			return nil
		}
	case "background_task_started":
		if validID(fact.SessionID) && validID(fact.ParentID) && fact.SessionID != fact.ParentID && noExtra(true, true, false, false, false, false, false) {
			return nil
		}
	case "background_task_settled":
		if validID(fact.SessionID) && (fact.Outcome == "completed" || fact.Outcome == "error") && noExtra(true, false, false, false, false, true, false) {
			return nil
		}
	case "context_usage":
		if validID(fact.SessionID) && validID(fact.MessageID) && fact.ContextTokens != nil && *fact.ContextTokens >= 0 && noExtra(true, false, false, true, false, false, true) {
			return nil
		}
	case "question_opened", "question_closed", "permission_opened", "permission_closed":
		if validID(fact.SessionID) && validID(fact.RequestID) && noExtra(true, false, true, false, false, false, false) {
			return nil
		}
	}
	return errors.New("invalid known OpenCode fact")
}
