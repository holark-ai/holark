package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/holark-ai/holark/internal/harness/claudecode"
)

type runtimeMetadata struct {
	LaunchToken      string `json:"launch_token"`
	HarnessSessionID string `json:"harness_session_id"`
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("2.1.220 (Claude Code)")
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	settingsPath := argument("--settings")
	sessionID := argument("--session-id")
	source := "startup"
	if sessionID == "" {
		sessionID = argument("--resume")
		source = "resume"
	}
	if settingsPath == "" || sessionID == "" {
		return errors.New("missing Claude fixture arguments")
	}
	runtimeDir := filepath.Dir(settingsPath)
	metadataData, err := os.ReadFile(filepath.Join(runtimeDir, claudecode.MetadataFileName))
	if err != nil {
		return err
	}
	var metadata runtimeMetadata
	if err := json.Unmarshal(metadataData, &metadata); err != nil {
		return err
	}
	appendEvent := func(event claudecode.NormalizedEvent) error {
		event.Version = 1
		event.HarnessSessionID = metadata.HarnessSessionID
		event.LaunchToken = metadata.LaunchToken
		file, err := os.OpenFile(filepath.Join(runtimeDir, claudecode.SpoolFileName), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		encodeErr := json.NewEncoder(file).Encode(event)
		closeErr := file.Close()
		if encodeErr != nil {
			return encodeErr
		}
		return closeErr
	}
	if err := appendEvent(claudecode.NormalizedEvent{Event: "SessionStart", SessionID: sessionID, Source: source}); err != nil {
		return err
	}
	fmt.Println("ready")
	scanner := bufio.NewScanner(os.Stdin)
	promptID := 0
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "exit" {
			fmt.Println("bye")
			return nil
		}
		if line == "n" {
			if err := appendEvent(claudecode.NormalizedEvent{
				Event: "Stop", SessionID: sessionID, BackgroundTasksKnown: true, SessionCronsKnown: true,
			}); err != nil {
				return err
			}
			fmt.Println("denied")
			continue
		}
		promptID++
		currentPromptID := fmt.Sprintf("prompt-%d", promptID)
		if err := appendEvent(claudecode.NormalizedEvent{
			Event: "UserPromptSubmit", SessionID: sessionID, PromptID: currentPromptID,
		}); err != nil {
			return err
		}
		if line == "permission" {
			if err := appendEvent(claudecode.NormalizedEvent{
				Event: "PermissionRequest", SessionID: sessionID, PromptID: currentPromptID,
				ToolName: "Bash", ToolUseID: fmt.Sprintf("tool-%d", promptID),
			}); err != nil {
				return err
			}
			fmt.Println("permission")
		}
	}
	return scanner.Err()
}

func argument(name string) string {
	for index := 1; index+1 < len(os.Args); index++ {
		if os.Args[index] == name {
			return os.Args[index+1]
		}
	}
	return ""
}
