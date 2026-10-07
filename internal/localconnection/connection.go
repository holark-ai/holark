// Package localconnection publishes and discovers authenticated local Holark
// server connections without conflating concurrently open repositories.
package localconnection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Connection struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// Publish writes the repository-scoped connection. The returned cleanup only
// removes a file still owned by this publication.
func Publish(directory, repositoryID string, connection Connection) (func(), error) {
	url, err := NormalizeURL(connection.URL)
	if err != nil || strings.TrimSpace(connection.Token) == "" {
		return nil, fmt.Errorf("invalid private connection")
	}
	connection.URL = url
	if err := validate(connection); err != nil {
		return nil, err
	}
	scopedPath, err := ScopedPath(directory, repositoryID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(scopedPath), 0o700); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(connection)
	if err != nil {
		return nil, err
	}
	return write(directory, scopedPath, payload)
}

// Read returns the connection published for repositoryID.
func Read(directory, repositoryID string) (Connection, bool) {
	scopedPath, err := ScopedPath(directory, repositoryID)
	if err != nil {
		return Connection{}, false
	}
	return read(scopedPath)
}

func ScopedPath(directory, repositoryID string) (string, error) {
	repositoryID = strings.TrimSpace(repositoryID)
	if repositoryID == "" || filepath.Base(repositoryID) != repositoryID || repositoryID == "." {
		return "", fmt.Errorf("invalid repository identity")
	}
	return filepath.Join(directory, "connections", repositoryID+".json"), nil
}

func write(temporaryDirectory, destination string, payload []byte) (func(), error) {
	temporary, err := os.CreateTemp(temporaryDirectory, ".connection-*")
	if err != nil {
		return nil, err
	}
	name := temporary.Name()
	fail := func(failure error) (func(), error) {
		_ = temporary.Close()
		_ = os.Remove(name)
		return nil, failure
	}
	if err = temporary.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err = temporary.Write(payload); err != nil {
		return fail(err)
	}
	if err = temporary.Close(); err != nil {
		_ = os.Remove(name)
		return nil, err
	}
	if err = os.Rename(name, destination); err != nil {
		_ = os.Remove(name)
		return nil, err
	}
	return func() {
		current, readErr := os.ReadFile(destination)
		if readErr == nil && bytes.Equal(current, payload) {
			_ = os.Remove(destination)
		}
	}, nil
}

func read(path string) (Connection, bool) {
	var connection Connection
	file, err := os.Open(path)
	if err != nil {
		return Connection{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return Connection{}, false
	}
	data, err := io.ReadAll(io.LimitReader(file, 16*1024))
	if err != nil || json.Unmarshal(data, &connection) != nil || validate(connection) != nil {
		return Connection{}, false
	}
	connection.URL, _ = NormalizeURL(connection.URL)
	return connection, true
}

func validate(connection Connection) error {
	if _, err := NormalizeURL(connection.URL); err != nil || strings.TrimSpace(connection.Token) == "" {
		return fmt.Errorf("invalid private connection")
	}
	return nil
}

// NormalizeURL validates and canonicalizes a loopback HTTP origin.
func NormalizeURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Port() == "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("Holark server URL must be a loopback HTTP origin")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("Holark server URL must be a loopback HTTP origin")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String(), nil
}
