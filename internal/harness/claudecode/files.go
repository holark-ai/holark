package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var errPresencePending = errors.New("Claude presence status has not initialized")

const maxTranscriptRecordBytes = 16 * 1024 * 1024
const transcriptReadBudget = 256 * 1024

// observedFiles watches selected files and the parents needed to reach them.
// It never enumerates other conversations.
type observedFiles struct {
	watcher        *fileWatcher
	presencePath   string
	transcriptPath string
	transcriptInfo os.FileInfo
	offset         int64
	partial        []byte
	launchedAt     time.Time
	pid            int
	launchPID      int
	presenceDir    string
	candidates     []int
	sessionID      string
	startedAt      int64
	updatedAt      int64
	presenceSeen   bool
}

func newObservedFiles(pid int, launchedAt time.Time) (*observedFiles, error) {
	if pid <= 0 || launchedAt.IsZero() {
		return nil, errors.New("Claude process identity is unavailable")
	}
	watcher, err := newFileWatcher()
	if err != nil {
		return nil, err
	}
	f := &observedFiles{watcher: watcher, launchPID: pid, launchedAt: launchedAt}
	return f, nil
}

func (f *observedFiles) watchParent(path string) error {
	dir := filepath.Dir(path)
	for {
		err := f.watcher.Add(dir)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) || dir == filepath.Dir(dir) {
			return err
		}
		dir = filepath.Dir(dir)
	}
}

func (f *observedFiles) refreshWatches(runtimeDir string) error {
	paths := append(f.presencePaths(), filepath.Join(runtimeDir, LossFilePrefix), f.transcriptPath)
	for _, path := range paths {
		if path != "" {
			if err := f.watchParent(path); err != nil {
				return err
			}
			// Linux directory watches report child writes and replacements.
			// Watching the file too can race fsnotify's removal of the old inode
			// during atomic replacement and deadlock while re-adding the watch.
			if runtime.GOOS == "linux" {
				continue
			}
			// Directory vnode events do not report appends to existing files.
			if err := f.watcher.Add(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func lossMarkerPaths(runtimeDir string) ([]string, error) {
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, LossFilePrefix) && len(name) > len(LossFilePrefix) {
			paths = append(paths, filepath.Join(runtimeDir, name))
		}
	}
	return paths, nil
}

func lossMarkersPresent(runtimeDir string) (bool, error) {
	paths, err := lossMarkerPaths(runtimeDir)
	return len(paths) != 0, err
}

func clearLossMarkers(runtimeDir, launchToken string) error {
	paths, err := lossMarkerPaths(runtimeDir)
	if err != nil {
		return err
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > MaxRecordBytes {
			return errors.New("invalid Claude hook loss marker")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(data) != launchToken {
			return errors.New("Claude hook loss marker does not match this launch")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func relevantPath(eventPath, target string) bool {
	if target == "" {
		return false
	}
	if eventPath == target {
		return true
	}
	for parent := filepath.Dir(target); ; parent = filepath.Dir(parent) {
		if eventPath == parent {
			return true
		}
		if parent == filepath.Dir(parent) {
			return false
		}
	}
}

func relevantLossMarkerPath(eventPath, runtimeDir string) bool {
	if eventPath == runtimeDir {
		return true
	}
	return filepath.Dir(eventPath) == runtimeDir && strings.HasPrefix(filepath.Base(eventPath), LossFilePrefix)
}

// Capture the hook's live ancestry before acknowledging it. Claude waits for
// that acknowledgement, so wrappers and hook shells are still in the tree.
func (f *observedFiles) discoverProcesses(hookPID int) error {
	var candidates []int
	seen := make(map[int]bool)
	for pid := hookPID; pid > 0 && !seen[pid] && len(candidates) < 64; {
		seen[pid] = true
		candidates = append(candidates, pid)
		if pid == f.launchPID {
			f.candidates = candidates
			f.pid, f.presencePath = 0, ""
			return nil
		}
		parent, err := processParent(pid)
		if err != nil {
			break
		}
		pid = parent
	}
	return errors.New("Claude startup hook does not belong to the launched process")
}

func (f *observedFiles) presencePaths() []string {
	if f.presencePath != "" {
		return []string{f.presencePath}
	}
	paths := make([]string, 0, len(f.candidates))
	for _, pid := range f.candidates {
		paths = append(paths, filepath.Join(f.presenceDir, fmt.Sprintf("%d.json", pid)))
	}
	return paths
}

func (f *observedFiles) relevantPresencePath(path string) bool {
	for _, target := range f.presencePaths() {
		if relevantPath(path, target) {
			return true
		}
	}
	return false
}

func (f *observedFiles) readPresence() (string, error) {
	if f.presencePath != "" {
		return f.readSelectedPresence()
	}
	// Only inspect ancestors of this launch's authenticated startup hook. A
	// matching session and fresh start time exclude wrapper and stale records,
	// including the source session of a fork or an earlier resumed process.
	var selected *observedFiles
	var status string
	var selectedErr error
	for i, path := range f.presencePaths() {
		candidate := *f
		candidate.pid, candidate.presencePath = f.candidates[i], path
		value, err := candidate.readSelectedPresence()
		if err != nil && !errors.Is(err, errPresencePending) {
			continue
		}
		if selected != nil {
			return "", errors.New("Claude process identity is ambiguous")
		}
		selected, status, selectedErr = &candidate, value, err
	}
	if selected == nil {
		return "", errPresencePending
	}
	*f = *selected
	return status, selectedErr
}

func (f *observedFiles) readSelectedPresence() (string, error) {
	info, err := os.Lstat(f.presencePath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0400 == 0 || info.Size() > MaxRecordBytes {
		return "", errors.New("Claude presence file is inaccessible or unsupported")
	}
	file, err := os.Open(f.presencePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxRecordBytes+1))
	if err != nil {
		return "", err
	}
	var p struct {
		PID       int    `json:"pid"`
		SessionID string `json:"sessionId"`
		StartedAt int64  `json:"startedAt"`
		UpdatedAt int64  `json:"statusUpdatedAt"`
		Status    string `json:"status"`
	}
	if len(data) > MaxRecordBytes || json.Unmarshal(data, &p) != nil {
		return "", errors.New("unsupported Claude presence record")
	}
	if p.PID != f.pid || p.SessionID != f.sessionID || p.StartedAt < f.launchedAt.Add(-2*time.Second).UnixMilli() || p.StartedAt > time.Now().Add(time.Second).UnixMilli() ||
		(f.startedAt != 0 && p.StartedAt != f.startedAt) {
		return "", errors.New("Claude presence identity or freshness does not match this launch")
	}
	if !f.presenceSeen && (p.UpdatedAt == 0 || p.Status == "") {
		return "", errPresencePending
	}
	// Claude can capture initial idle status before recording startedAt. Both
	// timestamps must belong to this launch; later observations must not rewind.
	if p.UpdatedAt < f.launchedAt.Add(-2*time.Second).UnixMilli() || p.UpdatedAt < f.updatedAt || p.UpdatedAt > time.Now().Add(time.Second).UnixMilli() {
		return "", errors.New("Claude presence identity or freshness does not match this launch")
	}
	switch p.Status {
	case "idle", "busy", "waiting", "shell":
	default:
		return "", errors.New("unsupported Claude presence status")
	}
	f.startedAt, f.updatedAt, f.presenceSeen = p.StartedAt, p.UpdatedAt, true
	return p.Status, nil
}

// On SessionStart discard existing history, including resumed/forked turns.
// Subsequent reads are bounded and explicitly rescheduled until fully drained.
func (f *observedFiles) setTranscript(path, sessionID string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("Claude transcript path is unavailable")
	}
	if path == f.transcriptPath && sessionID == f.sessionID {
		return nil
	}
	f.sessionID, f.transcriptPath = sessionID, path
	f.partial, f.offset, f.transcriptInfo = nil, 0, nil
	f.startedAt, f.updatedAt, f.presenceSeen = 0, 0, false
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("Claude transcript is not a regular file")
	}
	f.transcriptInfo, f.offset = info, info.Size()
	return nil
}

func (f *observedFiles) readTranscript(apply func(TranscriptObservation)) (bool, error) {
	if f.transcriptPath == "" {
		return false, nil
	}
	info, err := os.Lstat(f.transcriptPath)
	if errors.Is(err, os.ErrNotExist) && f.transcriptInfo == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0400 == 0 {
		return false, errors.New("Claude transcript is inaccessible")
	}
	if f.transcriptInfo != nil && (!os.SameFile(info, f.transcriptInfo) || info.Size() < f.offset) {
		// History continuity was lost. Do not replay old Stop records as new evidence.
		f.transcriptInfo, f.offset, f.partial = info, info.Size(), nil
		return false, errors.New("Claude transcript was replaced or truncated")
	}
	f.transcriptInfo = info
	if info.Size() == f.offset {
		return false, nil
	}
	file, err := os.Open(f.transcriptPath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(info, current) {
		return false, errors.New("Claude transcript changed during read")
	}
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, transcriptReadBudget))
	if err != nil {
		return false, err
	}
	f.offset += int64(len(data))
	f.partial = append(f.partial, data...)
	for {
		end := bytes.IndexByte(f.partial, '\n')
		if end < 0 {
			break
		}
		if end > maxTranscriptRecordBytes {
			return false, errors.New("Claude transcript record exceeds size limit")
		}
		record, err := decodeTranscript(f.partial[:end])
		if err != nil {
			return false, err
		}
		if !record.Timestamp.Before(f.launchedAt) {
			apply(record)
		}
		f.partial = f.partial[end+1:]
	}
	if len(f.partial) > maxTranscriptRecordBytes {
		return false, errors.New("Claude transcript record exceeds size limit")
	}
	if len(f.partial) == 0 {
		f.partial = nil
	}
	return len(data) == transcriptReadBudget, nil
}

func decodeTranscript(data []byte) (TranscriptObservation, error) {
	// Decode only structural fields; never retain prompts, responses or tool data.
	var r struct {
		Type                  string          `json:"type"`
		Subtype               string          `json:"subtype"`
		SessionID             string          `json:"sessionId"`
		Sidechain             bool            `json:"isSidechain"`
		Timestamp             time.Time       `json:"timestamp"`
		InterruptedMessageID  string          `json:"interruptedMessageId"`
		HasOutput             *bool           `json:"hasOutput"`
		HookErrors            json.RawMessage `json:"hookErrors"`
		AdditionalContext     json.RawMessage `json:"hookAdditionalContext"`
		PreventedContinuation bool            `json:"preventedContinuation"`
		Attachment            struct {
			Type      string `json:"type"`
			HookEvent string `json:"hookEvent"`
		} `json:"attachment"`
		Message struct {
			Role  string `json:"role"`
			Usage *struct {
				InputTokens              int64 `json:"input_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return TranscriptObservation{}, errors.New("unsupported Claude transcript record")
	}
	context := bytes.TrimSpace(r.AdditionalContext)
	observation := TranscriptObservation{SessionID: r.SessionID, Sidechain: r.Sidechain, Timestamp: r.Timestamp,
		Interrupted: r.InterruptedMessageID != "", StopSummary: r.Type == "system" && r.Subtype == "stop_hook_summary" && r.HasOutput != nil,
		HasOutput: r.HasOutput != nil && *r.HasOutput, HasErrors: len(r.HookErrors) == 0 || string(bytes.TrimSpace(r.HookErrors)) != "[]", PreventedContinuation: r.PreventedContinuation,
		AdditionalContext: len(context) != 0 && string(context) != "null" && string(context) != "[]" && string(context) != `""`,
	}
	if r.Attachment.HookEvent == "Stop" {
		observation.StopOutcome = r.Attachment.Type
	}
	if r.Type == "assistant" && r.Message.Role == "assistant" && r.Message.Usage != nil {
		usage := r.Message.Usage
		const maxInt64 = int64(^uint64(0) >> 1)
		if usage.InputTokens >= 0 && usage.CacheCreationInputTokens >= 0 && usage.CacheReadInputTokens >= 0 &&
			usage.InputTokens <= maxInt64-usage.CacheCreationInputTokens {
			total := usage.InputTokens + usage.CacheCreationInputTokens
			if total <= maxInt64-usage.CacheReadInputTokens {
				total += usage.CacheReadInputTokens
				observation.ContextTokens = &total
			}
		}
	}
	return observation, nil
}
