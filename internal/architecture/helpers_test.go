package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	return filepath.Clean(filepath.Join(dir, "..", ".."))
}
func assertNoImports(t *testing.T, path string, forbidden ...string) {
	t.Helper()
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	text := string(data)
	for _, value := range forbidden {
		if strings.Contains(text, "\"github.com/holark-ai/holark/"+value+"\"") {
			t.Errorf("%s imports forbidden %s", path, value)
		}
	}
}
