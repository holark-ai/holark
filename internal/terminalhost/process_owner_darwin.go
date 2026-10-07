//go:build darwin

package terminalhost

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func processOwnerEntries(owners map[string]bool) ([]ownedProcess, error) {
	entries, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	var result []ownedProcess
	for _, entry := range entries {
		if int(entry.Eproc.Ucred.Uid) != os.Getuid() || entry.Proc.P_stat == darwinZombieState {
			continue
		}
		pid := int(entry.Proc.P_pid)
		env, err := unix.SysctlRaw("kern.procargs2", pid)
		result = append(result, ownedProcess{pid: pid, parent: int(entry.Eproc.Ppid), started: fmt.Sprint(entry.Proc.P_starttime), owned: err == nil && hasAnyProcessOwner(env, owners)})
	}
	return result, nil
}

func processOwnerIdentity(pid int) string {
	entry, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || entry.Proc.P_pid != int32(pid) {
		return ""
	}
	return fmt.Sprint(entry.Proc.P_starttime)
}
