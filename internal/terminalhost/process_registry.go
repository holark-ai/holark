package terminalhost

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ProcessRegistry journals ownership before launch. Its directory must be protected
// by the application's instance lock. Old records are retained until cleanup succeeds.
type ProcessRegistry struct {
	mu        sync.Mutex
	directory string
	previous  map[string]processRecord
}

type processRecord struct {
	Owner   string `json:"owner"`
	PID     int    `json:"pid,omitempty"`
	Started string `json:"started,omitempty"`
}

func OpenProcessRegistry(directory string) (*ProcessRegistry, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	r := &ProcessRegistry{directory: directory, previous: map[string]processRecord{}}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		var record processRecord
		if err = json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		owner, err := hex.DecodeString(record.Owner)
		if err != nil || len(owner) != 32 || entry.Name() != record.Owner+".json" {
			return nil, errors.New("invalid process ownership record")
		}
		r.previous[record.Owner] = record
	}
	return r, nil
}

// Recover only cleans up launches from before this registry was opened. All
// launch paths call it, so failed cleanup also fences new work until a retry succeeds.
func (r *ProcessRegistry) Recover() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.previous) == 0 {
		return nil
	}
	owners := make([]string, 0, len(r.previous))
	known := make([]ownedProcess, 0, len(r.previous))
	for owner, record := range r.previous {
		owners = append(owners, owner)
		known = append(known, ownedProcess{pid: record.PID, started: record.Started})
	}
	// One scan covers every old owner, including descendants of vanished parents.
	if err := reapOwnedProcesses(owners, 300*time.Millisecond, known...); err != nil {
		return fmt.Errorf("clean up previous Holark processes: %w", err)
	}
	for owner := range r.previous {
		if err := os.Remove(filepath.Join(r.directory, owner+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(r.previous, owner)
	}
	return nil
}

func (r *ProcessRegistry) Prepare(command *exec.Cmd) (string, error) {
	if r == nil {
		return "", nil
	}
	if err := r.Recover(); err != nil {
		return "", err
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	owner := hex.EncodeToString(bytes)
	if err := r.save(processRecord{Owner: owner}); err != nil {
		return "", err
	}
	environment := command.Env
	if environment == nil {
		environment = os.Environ()
	}
	command.Env = nil
	for _, entry := range environment {
		if !strings.HasPrefix(entry, ProcessOwnerEnvironment+"=") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, ProcessOwnerEnvironment+"="+owner)
	return owner, nil
}

func (r *ProcessRegistry) Started(owner string, pid int) error {
	if r == nil {
		return nil
	}
	return r.save(processRecord{Owner: owner, PID: pid, Started: processOwnerIdentity(pid)})
}

func (r *ProcessRegistry) save(record processRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(r.directory, ".process-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), filepath.Join(r.directory, record.Owner+".json")); err != nil {
		return err
	}
	directory, err := os.Open(r.directory)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
