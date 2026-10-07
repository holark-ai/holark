//go:build !linux && !darwin

package claudecode

import "errors"

func processParent(int) (int, error) {
	return 0, errors.New("Claude process discovery is unsupported on this platform")
}
