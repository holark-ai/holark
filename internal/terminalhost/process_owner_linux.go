//go:build linux

package terminalhost

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var processOwnerBootID = func() string {
	data, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(data))
}()

func processOwnerStat(pid int) (ownedProcess, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ownedProcess{}, false
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return ownedProcess{}, false
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" {
		return ownedProcess{}, false
	}
	parent, err := strconv.Atoi(fields[1])
	return ownedProcess{pid: pid, parent: parent, started: processOwnerBootID + ":" + fields[19]}, err == nil
}

func processOwnerEntries(owners map[string]bool) ([]ownedProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var result []ownedProcess
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		info, err := os.Stat(filepath.Join("/proc", entry.Name()))
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Getuid() {
			continue
		}
		p, ok := processOwnerStat(pid)
		if !ok {
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		p.owned = err == nil && hasAnyProcessOwner(env, owners)
		result = append(result, p)
	}
	return result, nil
}

func processOwnerIdentity(pid int) string {
	p, ok := processOwnerStat(pid)
	if !ok {
		return ""
	}
	return p.started
}
