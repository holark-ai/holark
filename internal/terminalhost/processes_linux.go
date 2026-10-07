//go:build linux

package terminalhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func configureChildProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func terminalProcessGroupIDs(sessionID int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	groups := make(map[int]struct{})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err == nil {
			commandEnd := strings.LastIndexByte(string(stat), ')')
			if commandEnd >= 0 && commandEnd+2 < len(stat) && stat[commandEnd+2] == 'Z' {
				continue
			}
		}
		processGroup, err := unix.Getpgid(pid)
		if err != nil || processGroup <= 0 {
			continue
		}
		sid, err := unix.Getsid(pid)
		if err != nil || sid != sessionID {
			continue
		}
		groups[processGroup] = struct{}{}
	}
	result := make([]int, 0, len(groups))
	for processGroup := range groups {
		result = append(result, processGroup)
	}
	return result, nil
}
