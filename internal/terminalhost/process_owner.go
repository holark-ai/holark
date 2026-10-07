package terminalhost

import (
	"bytes"
	"errors"
	"syscall"
	"time"
)

// ProcessOwnerEnvironment marks server-owned subprocesses, including tools
// which create their own OS sessions. It carries no endpoint credential.
const ProcessOwnerEnvironment = "HOLARK_PROCESS_OWNER"

type ownedProcess struct {
	pid, parent int
	started     string
	owned       bool
}

// ReapOwnedProcesses is a shutdown-only fallback after the server has exited.
// It does not discover conversations or run during ordinary observation. Match
// a private per-server ownership marker and verify start identity before signals
// so unrelated servers and reused PIDs are excluded.
func ReapOwnedProcesses(owner string) error {
	if owner == "" {
		return errors.New("process owner is required")
	}
	return reapOwnedProcesses([]string{owner}, 0)
}

func hasAnyProcessOwner(environment []byte, owners map[string]bool) bool {
	prefix := []byte(ProcessOwnerEnvironment + "=")
	for _, entry := range bytes.Split(environment, []byte{0}) {
		if bytes.HasPrefix(entry, prefix) && owners[string(entry[len(prefix):])] {
			return true
		}
	}
	return false
}

func reapOwnedProcesses(owners []string, grace time.Duration, known ...ownedProcess) error {
	forceAt := time.Now().Add(grace)
	deadline := forceAt.Add(2 * time.Second)
	ownerSet := make(map[string]bool, len(owners))
	for _, owner := range owners {
		ownerSet[owner] = true
	}
	// Remember exact identities across scans: a child may clear its environment
	// and become orphaned after its parent receives the first signal.
	identities := make(map[int]string, len(known))
	for _, p := range known {
		if p.pid > 0 && p.started != "" {
			identities[p.pid] = p.started
		}
	}
	for {
		entries, err := processOwnerEntries(ownerSet)
		if err != nil {
			return err
		}
		owned := map[int]bool{}
		for _, p := range entries {
			if p.owned || (p.started != "" && identities[p.pid] == p.started) {
				owned[p.pid] = true
			}
		}
		for changed := true; changed; {
			changed = false
			for _, p := range entries {
				if !owned[p.pid] && owned[p.parent] {
					owned[p.pid] = true
					changed = true
				}
			}
		}
		if len(owned) == 0 {
			return nil
		}
		signal := syscall.SIGKILL
		if time.Now().Before(forceAt) {
			signal = syscall.SIGTERM
		}
		for _, p := range entries {
			if owned[p.pid] && p.started != "" {
				identities[p.pid] = p.started
			}
		}
		for _, p := range entries {
			if owned[p.pid] && p.started != "" && processOwnerIdentity(p.pid) == p.started {
				if err = syscall.Kill(p.pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
					return err
				}
			}
		}
		if time.Now().After(deadline) {
			return ErrWaitTimeout
		}
		time.Sleep(100 * time.Millisecond)
	}
}
