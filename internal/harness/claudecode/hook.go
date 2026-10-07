package claudecode

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/protocol"
)

const (
	// Claude includes content-bearing tool and assistant fields in hook input;
	// retain a finite multi-megabyte envelope before normalizing them away.
	MaxHookInputBytes = 16 * 1024 * 1024
	MaxRecordBytes    = 32 * 1024

	droppedHookDiagnostic = "hook_event_dropped"
)

type IngestSpec struct {
	RuntimeDir       string
	LaunchToken      string
	HarnessSessionID string
}

// NormalizedEvent is the content-free event contract between the short-lived
// Claude hook process and Holark's monitor. It deliberately excludes prompt,
// response, tool input, tool output, and notification content.
type NormalizedEvent struct {
	ConfigDir            string `json:"config_dir,omitempty"`
	ProcessID            int    `json:"process_id,omitempty"`
	SentAt               int64  `json:"sent_at"`
	Version              int    `json:"version"`
	HarnessSessionID     string `json:"harness_session_id"`
	LaunchToken          string `json:"launch_token"`
	Event                string `json:"event"`
	SessionID            string `json:"session_id,omitempty"`
	PromptID             string `json:"prompt_id,omitempty"`
	TranscriptPath       string `json:"transcript_path,omitempty"`
	CWD                  string `json:"cwd,omitempty"`
	Source               string `json:"source,omitempty"`
	AgentType            string `json:"agent_type,omitempty"`
	AgentID              string `json:"agent_id,omitempty"`
	ToolName             string `json:"tool_name,omitempty"`
	ToolUseID            string `json:"tool_use_id,omitempty"`
	NotificationType     string `json:"notification_type,omitempty"`
	ElicitationID        string `json:"elicitation_id,omitempty"`
	MCPServerName        string `json:"mcp_server_name,omitempty"`
	Action               string `json:"action,omitempty"`
	BackgroundTasksKnown bool   `json:"background_tasks_known,omitempty"`
	BackgroundTaskCount  int    `json:"background_task_count,omitempty"`
	SessionCronsKnown    bool   `json:"session_crons_known,omitempty"`
	SessionCronCount     int    `json:"session_cron_count,omitempty"`
	Interrupted          bool   `json:"interrupted,omitempty"`
	Diagnostic           string `json:"diagnostic,omitempty"`
}

func IngestHook(input io.Reader, spec IngestSpec) error {
	if input == nil || !validRuntimeDir(spec.RuntimeDir) || !protocol.ValidIdentifier(spec.HarnessSessionID) {
		return errors.New("invalid Claude hook invocation")
	}
	info, err := os.Lstat(spec.RuntimeDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("insecure Claude hook runtime")
	}
	metadata, err := readRuntimeMetadata(spec.RuntimeDir)
	if err != nil {
		return fmt.Errorf("read Claude hook runtime: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(metadata.LaunchToken), []byte(spec.LaunchToken)) != 1 || metadata.HarnessSessionID != spec.HarnessSessionID {
		return errors.New("Claude hook launch identity does not match")
	}
	data, err := io.ReadAll(io.LimitReader(input, MaxHookInputBytes+1))
	if err != nil {
		return recordDroppedHook(spec.RuntimeDir, metadata, err)
	}
	if len(data) == 0 || len(data) > MaxHookInputBytes {
		return recordDroppedHook(spec.RuntimeDir, metadata, errors.New("Claude hook input exceeds size limit"))
	}
	event, err := NormalizeHook(data, metadata)
	if err != nil {
		return recordDroppedHook(spec.RuntimeDir, metadata, err)
	}
	if event.Event == "SessionStart" && event.AgentID == "" {
		event.ProcessID = os.Getpid()
		// The hook inherits Claude's environment after launch wrappers have
		// applied their overrides; Holark's own environment may differ.
		configDir := os.Getenv("CLAUDE_CONFIG_DIR")
		if configDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return recordDroppedHook(spec.RuntimeDir, metadata, err)
			}
			configDir = filepath.Join(home, ".claude")
		}
		event.ConfigDir, err = filepath.Abs(configDir)
		if err != nil {
			return recordDroppedHook(spec.RuntimeDir, metadata, err)
		}
	}
	record, err := json.Marshal(event)
	if err != nil {
		return recordDroppedHook(spec.RuntimeDir, metadata, err)
	}
	if len(record)+1 > MaxRecordBytes {
		return recordDroppedHook(spec.RuntimeDir, metadata, errors.New("normalized Claude hook event exceeds size limit"))
	}
	record = append(record, '\n')
	if err := deliverHook(metadata.SocketPath, record); err != nil {
		return recordDroppedHook(spec.RuntimeDir, metadata, err)
	}
	return nil
}

func NormalizeHook(data []byte, metadata runtimeMetadata) (NormalizedEvent, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return NormalizedEvent{}, errors.New("Claude hook input must be a JSON object")
	}
	var input struct {
		Event            string          `json:"hook_event_name"`
		SessionID        string          `json:"session_id"`
		PromptID         string          `json:"prompt_id"`
		TranscriptPath   string          `json:"transcript_path"`
		CWD              string          `json:"cwd"`
		Source           string          `json:"source"`
		AgentType        string          `json:"agent_type"`
		AgentID          string          `json:"agent_id"`
		ToolName         string          `json:"tool_name"`
		ToolUseID        string          `json:"tool_use_id"`
		NotificationType string          `json:"notification_type"`
		ElicitationID    string          `json:"elicitation_id"`
		MCPServerName    string          `json:"mcp_server_name"`
		Action           string          `json:"action"`
		BackgroundTasks  json.RawMessage `json:"background_tasks"`
		Interrupted      bool            `json:"is_interrupt"`
		SessionCrons     json.RawMessage `json:"session_crons"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		return NormalizedEvent{}, fmt.Errorf("decode Claude hook input: %w", err)
	}
	fields := []struct {
		name  string
		value string
		limit int
	}{
		{"hook_event_name", input.Event, 128},
		{"session_id", input.SessionID, 128},
		{"prompt_id", input.PromptID, 256},
		{"transcript_path", input.TranscriptPath, 4096},
		{"cwd", input.CWD, 4096},
		{"source", input.Source, 128},
		{"agent_type", input.AgentType, 256},
		{"agent_id", input.AgentID, 256},
		{"tool_name", input.ToolName, 256},
		{"tool_use_id", input.ToolUseID, 256},
		{"notification_type", input.NotificationType, 128},
		{"elicitation_id", input.ElicitationID, 256},
		{"mcp_server_name", input.MCPServerName, 256},
		{"action", input.Action, 128},
	}
	for _, field := range fields {
		if len(field.value) > field.limit || strings.Contains(field.value, "\x00") || !utf8.ValidString(field.value) {
			return NormalizedEvent{}, fmt.Errorf("invalid Claude hook %s", field.name)
		}
	}
	if input.Event == "" || input.SessionID == "" {
		return NormalizedEvent{}, errors.New("Claude hook event identity is missing")
	}
	event := NormalizedEvent{
		SentAt: time.Now().UnixMilli(), Version: 1, HarnessSessionID: metadata.HarnessSessionID, LaunchToken: metadata.LaunchToken,
		Event: input.Event, Interrupted: input.Interrupted, SessionID: input.SessionID, TranscriptPath: input.TranscriptPath, CWD: input.CWD,
		PromptID: input.PromptID,
		Source:   input.Source, AgentType: input.AgentType, AgentID: input.AgentID, ToolName: input.ToolName,
		ToolUseID: input.ToolUseID, NotificationType: input.NotificationType, ElicitationID: input.ElicitationID,
		MCPServerName: input.MCPServerName, Action: input.Action,
	}
	if input.BackgroundTasks != nil {
		count, err := arrayCount(input.BackgroundTasks)
		if err != nil {
			return NormalizedEvent{}, errors.New("invalid Claude hook background tasks")
		}
		event.BackgroundTasksKnown = true
		event.BackgroundTaskCount = count
	}
	if input.SessionCrons != nil {
		count, err := arrayCount(input.SessionCrons)
		if err != nil {
			return NormalizedEvent{}, errors.New("invalid Claude hook session crons")
		}
		event.SessionCronsKnown = true
		event.SessionCronCount = count
	}
	return event, nil
}

func arrayCount(data json.RawMessage) (int, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil || len(values) > 1024 {
		return 0, errors.New("invalid bounded array")
	}
	return len(values), nil
}

// A persistent, content-free loss marker per failure makes concurrent delivery
// loss visible even when the receiver was unavailable. These are not a journal.
func recordDroppedHook(runtimeDir string, metadata runtimeMetadata, cause error) error {
	file, err := os.CreateTemp(runtimeDir, LossFilePrefix+"*")
	if err != nil {
		return errors.Join(cause, err)
	}
	path := file.Name()
	if _, err = file.Write([]byte(metadata.LaunchToken)); err == nil {
		err = file.Close()
	} else {
		err = errors.Join(err, file.Close())
	}
	if err != nil {
		_ = os.Remove(path)
		return errors.Join(cause, err)
	}
	return cause
}

func deliverHook(path string, record []byte) error {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	if _, err := io.Copy(conn, bytes.NewReader(record)); err != nil {
		return err
	}
	var ack [3]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return err
	}
	if string(ack[:]) != "ok\n" {
		return errors.New("Claude hook was rejected")
	}
	return nil
}
