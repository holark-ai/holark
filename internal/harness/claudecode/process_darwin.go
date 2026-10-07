package claudecode

import (
	"errors"

	"golang.org/x/sys/unix"
)

func processParent(pid int) (int, error) {
	entry, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if int(entry.Proc.P_pid) != pid || entry.Proc.P_stat == 5 { // SZOMB
		return 0, errors.New("process is unavailable")
	}
	return int(entry.Eproc.Ppid), nil
}
