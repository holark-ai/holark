package localconnection

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryConnectionsRemainIndependent(t *testing.T) {
	directory := t.TempDir()
	first := Connection{URL: "http://127.0.0.1:41001", Token: "first-token"}
	second := Connection{URL: "http://127.0.0.1:41002", Token: "second-token"}

	cleanupFirst, err := Publish(directory, "first-repository", first)
	if err != nil {
		t.Fatal(err)
	}
	cleanupSecond, err := Publish(directory, "second-repository", second)
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := Read(directory, "first-repository"); !ok || got != first {
		t.Fatalf("first connection = %+v, available = %t", got, ok)
	}
	if got, ok := Read(directory, "second-repository"); !ok || got != second {
		t.Fatalf("second connection = %+v, available = %t", got, ok)
	}
	if got, ok := Read(directory, "missing-repository"); ok || got != (Connection{}) {
		t.Fatalf("missing connection = %+v, available = %t", got, ok)
	}

	cleanupFirst()
	if got, ok := Read(directory, "second-repository"); !ok || got != second {
		t.Fatalf("second connection after first cleanup = %+v, available = %t", got, ok)
	}
	cleanupSecond()
	secondPath, _ := ScopedPath(directory, "second-repository")
	if _, err := os.Stat(secondPath); !os.IsNotExist(err) {
		t.Fatalf("second scoped connection remains: %v", err)
	}

	firstPath, _ := ScopedPath(directory, "first-repository")
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("first scoped connection remains: %v", err)
	}
}

func TestCleanupDoesNotRemoveNewerPublication(t *testing.T) {
	directory := t.TempDir()
	first := Connection{URL: "http://127.0.0.1:41001", Token: "first-token"}
	second := Connection{URL: "http://127.0.0.1:41002", Token: "second-token"}
	cleanupFirst, err := Publish(directory, "repository", first)
	if err != nil {
		t.Fatal(err)
	}
	cleanupSecond, err := Publish(directory, "repository", second)
	if err != nil {
		t.Fatal(err)
	}

	cleanupFirst()
	if got, ok := Read(directory, "repository"); !ok || got != second {
		t.Fatalf("connection after stale cleanup = %+v, available = %t", got, ok)
	}
	cleanupSecond()
	path, _ := ScopedPath(directory, "repository")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("connection remains after owner cleanup: %v", err)
	}
}

func TestReadRejectsNonPrivateConnectionFile(t *testing.T) {
	directory := t.TempDir()
	path, err := ScopedPath(directory, "repository")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(`{"url":"http://127.0.0.1:41001","token":"secret"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if connection, ok := Read(directory, "repository"); ok || connection != (Connection{}) {
		t.Fatalf("connection = %+v, available = %t", connection, ok)
	}
}
