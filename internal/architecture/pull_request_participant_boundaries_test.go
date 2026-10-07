package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPullRequestParticipantDomainDoesNotImportOuterAdapters(t *testing.T) {
	domain := filepath.Join(repositoryRoot(t), "internal", "pullrequestparticipants")
	entries, err := os.ReadDir(domain)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(domain, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			name := strings.Trim(imported.Path.Value, "\"")
			for _, forbidden := range []string{"internal/httpapi", "internal/controlplane", "internal/store", "sqliteadapter", "internal/codehost/github/pullrequestparticipants", "database/sql"} {
				if strings.Contains(name, forbidden) {
					t.Errorf("%s imports forbidden dependency %q", path, name)
				}
			}
		}
	}
}
