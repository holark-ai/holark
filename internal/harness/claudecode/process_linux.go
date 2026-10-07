package claudecode

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processParent(pid int) (int, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, errors.New("invalid process identity")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 2 || fields[0] == "Z" {
		return 0, errors.New("process is unavailable")
	}
	return strconv.Atoi(fields[1])
}
