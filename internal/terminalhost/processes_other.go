//go:build !darwin && !linux

package terminalhost

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureChildProcess(*exec.Cmd) {}

func terminalProcessGroupIDs(sessionID int) ([]int, error) {
	err := syscall.Kill(-sessionID, 0)
	if errors.Is(err, syscall.ESRCH) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []int{sessionID}, nil
}
