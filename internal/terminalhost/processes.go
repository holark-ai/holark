package terminalhost

import (
	"errors"
	"syscall"
	"time"
)

// ReapProcessSession stops remaining process groups in an OS session owned by
// the caller. The session leader must have been launched with Setsid, and reaped
// before calling this. Conversation identity is never inferred from processes.
func ReapProcessSession(sessionID int) error {
	return forcePTYSessionExit(terminalProcessGroupIDs, sessionID, time.Second)
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	return syscall.Kill(-pid, signal)
}

func signalProcessGroupIfPresent(pid int, signal syscall.Signal) error {
	err := signalProcessGroup(pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func signalPTYSession(scanner processGroupScanner, sessionID int, signal syscall.Signal) error {
	processGroups, err := scanner(sessionID)
	if err != nil {
		return signalProcessGroupIfPresent(sessionID, signal)
	}
	for _, processGroup := range processGroups {
		if err := syscall.Kill(-processGroup, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	return nil
}

func waitForPTYSessionExit(scanner processGroupScanner, sessionID int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		processGroups, err := scanner(sessionID)
		if err != nil {
			return err
		}
		if len(processGroups) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return ErrWaitTimeout
		}
		time.Sleep(processPollInterval)
	}
}

func forcePTYSessionExit(scanner processGroupScanner, sessionID int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		processGroups, err := scanner(sessionID)
		if err != nil {
			return signalProcessGroupIfPresent(sessionID, syscall.SIGKILL)
		}
		if len(processGroups) == 0 {
			return nil
		}
		for _, processGroup := range processGroups {
			if err := syscall.Kill(-processGroup, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
		}
		if !time.Now().Before(deadline) {
			return ErrWaitTimeout
		}
		time.Sleep(processPollInterval)
	}
}

func drainPTYSession(scanner processGroupScanner, sessionID int) {
	_ = signalPTYSession(scanner, sessionID, syscall.SIGHUP)
	waitErr := waitForPTYSessionExit(scanner, sessionID, processExitGrace)
	if waitErr == nil {
		return
	}
	if !errors.Is(waitErr, ErrWaitTimeout) {
		_ = signalProcessGroupIfPresent(sessionID, syscall.SIGKILL)
		return
	}
	_ = forcePTYSessionExit(scanner, sessionID, time.Second)
}

func terminatePTYSession(scanner processGroupScanner, sessionID int, done <-chan struct{}, grace time.Duration) error {
	if grace <= 0 {
		grace = 2 * time.Second
	}
	if err := signalPTYSession(scanner, sessionID, syscall.SIGHUP); err != nil {
		return err
	}
	if waitForDone(done, grace) == nil {
		return nil
	}
	if err := forcePTYSessionExit(scanner, sessionID, time.Second); err != nil {
		return err
	}
	return waitForDone(done, time.Second)
}

func waitForDone(done <-chan struct{}, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return ErrWaitTimeout
	}
}
