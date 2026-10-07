package terminalhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	RendezvousVersion  = 1
	SocketFileName     = "host.sock"
	CredentialFileName = "credential"
	IdentityFileName   = "identity"
	StateFileName      = "state.json"
)

type Rendezvous struct {
	Version    int       `json:"version"`
	HostID     string    `json:"host_id"`
	SocketPath string    `json:"socket_path"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	Token      string    `json:"-"`
}

func EnsureRuntimeDirectory(directory string) error {
	if strings.TrimSpace(directory) == "" {
		return errors.New("terminal host runtime directory is required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("terminal host runtime directory must be a private directory")
	}
	return nil
}

// PrepareRendezvous replaces stale credentials before a new host is started.
// Callers serialize this operation with their node-local launch lock.
func PrepareRendezvous(directory string) (Rendezvous, error) {
	if err := EnsureRuntimeDirectory(directory); err != nil {
		return Rendezvous{}, err
	}
	for _, name := range []string{SocketFileName, StateFileName, CredentialFileName, IdentityFileName} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Rendezvous{}, err
		}
	}
	hostID, err := randomCredential(24)
	if err != nil {
		return Rendezvous{}, err
	}
	token, err := randomCredential(32)
	if err != nil {
		return Rendezvous{}, err
	}
	if err := writePrivateFile(filepath.Join(directory, IdentityFileName), []byte(hostID+"\n")); err != nil {
		return Rendezvous{}, err
	}
	if err := writePrivateFile(filepath.Join(directory, CredentialFileName), []byte(token+"\n")); err != nil {
		return Rendezvous{}, err
	}
	return Rendezvous{
		Version: RendezvousVersion, HostID: hostID, Token: token,
		SocketPath: filepath.Join(directory, SocketFileName),
	}, nil
}

func LoadHostConfiguration(directory string) (Rendezvous, error) {
	if err := validateRuntimeDirectory(directory); err != nil {
		return Rendezvous{}, err
	}
	hostID, err := readPrivateText(filepath.Join(directory, IdentityFileName))
	if err != nil {
		return Rendezvous{}, err
	}
	token, err := readPrivateText(filepath.Join(directory, CredentialFileName))
	if err != nil {
		return Rendezvous{}, err
	}
	configuration := Rendezvous{
		Version: RendezvousVersion, HostID: hostID, Token: token,
		SocketPath: filepath.Join(directory, SocketFileName),
	}
	if err := configuration.validate(directory, false); err != nil {
		return Rendezvous{}, err
	}
	return configuration, nil
}

func PublishRendezvous(directory string, state Rendezvous) error {
	if err := state.validate(directory, true); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return writePrivateFile(filepath.Join(directory, StateFileName), encoded)
}

func DiscoverRendezvous(directory string) (Rendezvous, error) {
	if err := validateRuntimeDirectory(directory); err != nil {
		return Rendezvous{}, err
	}
	data, err := readPrivateFile(filepath.Join(directory, StateFileName))
	if err != nil {
		return Rendezvous{}, err
	}
	var state Rendezvous
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return Rendezvous{}, err
	}
	state.Token, err = readPrivateText(filepath.Join(directory, CredentialFileName))
	if err != nil {
		return Rendezvous{}, err
	}
	identity, err := readPrivateText(filepath.Join(directory, IdentityFileName))
	if err != nil {
		return Rendezvous{}, err
	}
	if identity != state.HostID {
		return Rendezvous{}, errors.New("terminal host identity does not match rendezvous state")
	}
	if err := state.validate(directory, true); err != nil {
		return Rendezvous{}, err
	}
	return state, nil
}

func RemoveRendezvous(directory string) error {
	if strings.TrimSpace(directory) == "" {
		return errors.New("terminal host runtime directory is required")
	}
	for _, name := range []string{SocketFileName, StateFileName, CredentialFileName, IdentityFileName} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (state Rendezvous) validate(directory string, requireProcess bool) error {
	if state.Version != RendezvousVersion || len(state.HostID) < 24 || len(state.HostID) > 128 ||
		len(state.Token) < 32 || len(state.Token) > 512 || state.SocketPath != filepath.Join(directory, SocketFileName) {
		return errors.New("invalid terminal host rendezvous state")
	}
	if len(state.SocketPath) > 100 {
		return errors.New("terminal host socket path is too long")
	}
	if requireProcess && (state.PID <= 0 || state.StartedAt.IsZero()) {
		return errors.New("terminal host rendezvous process is invalid")
	}
	return nil
}

func validateRuntimeDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("terminal host runtime directory is not private")
	}
	return nil
}

func readPrivateText(path string) (string, error) {
	data, err := readPrivateFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if strings.ContainsRune(value, '\x00') || strings.ContainsRune(value, '\n') || value == "" {
		return "", errors.New("invalid terminal host private file")
	}
	return value, nil
}

func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("terminal host file %s must be private and regular", filepath.Base(path))
	}
	return os.ReadFile(path)
}
