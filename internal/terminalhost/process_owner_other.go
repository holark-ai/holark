//go:build !darwin && !linux

package terminalhost

import "errors"

func processOwnerEntries(map[string]bool) ([]ownedProcess, error) {
	return nil, errors.New("process ownership cleanup is unsupported on this platform")
}
func processOwnerIdentity(int) string { return "" }
