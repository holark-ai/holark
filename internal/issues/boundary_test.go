package issues_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestIssuePackagesDoNotImportSessionOrControlPlaneBoundaries(t *testing.T) {
	forbidden := []string{
		"github.com/holark-ai/holark/internal/controlplane",
		"github.com/holark-ai/holark/internal/issuesessions",
		"github.com/holark-ai/holark/internal/protocol",
		"github.com/holark-ai/holark/internal/sessions",
		"github.com/holark-ai/holark/internal/terminals",
	}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Decls {
			declaration, ok := imported.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range declaration.Specs {
				pathSpec, ok := spec.(*ast.ImportSpec)
				if !ok {
					continue
				}
				importPath, err := strconv.Unquote(pathSpec.Path.Value)
				if err != nil {
					return err
				}
				for _, prefix := range forbidden {
					if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
						t.Errorf("%s imports forbidden issue boundary %q", path, importPath)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
