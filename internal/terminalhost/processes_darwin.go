//go:build darwin

package terminalhost

import (
	"golang.org/x/sys/unix"
	"os/exec"
)

func configureChildProcess(*exec.Cmd) {}

const darwinZombieState = 5

func terminalProcessGroupIDs(sessionID int) ([]int, error) {
	entries, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	groups := make(map[int]struct{})
	for _, entry := range entries {
		pid := int(entry.Proc.P_pid)
		if pid <= 0 || entry.Proc.P_stat == darwinZombieState {
			continue
		}
		sid, err := unix.Getsid(pid)
		processGroup := int(entry.Eproc.Pgid)
		if err == nil && sid == sessionID && processGroup > 0 {
			groups[processGroup] = struct{}{}
		}
	}
	result := make([]int, 0, len(groups))
	for processGroup := range groups {
		result = append(result, processGroup)
	}
	return result, nil
}
