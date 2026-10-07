package localshell

import (
	"path/filepath"
	"testing"
)

func TestDefaultHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := DefaultHomeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".holark")
	if got != want {
		t.Fatalf("DefaultHomeDirectory() = %q, want %q", got, want)
	}
}
