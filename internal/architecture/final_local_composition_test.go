package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostedSpineIsAbsent(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{"cmd/holark-server", "cmd/holark-node", "deploy", "internal/auth", "internal/controlplane", "internal/gitssh", "internal/httpapi", "internal/iderelay", "internal/nodeclient", "internal/store", "internal/terminals/nodeadapter"} {
		if _, e := os.Stat(filepath.Join(root, path)); !os.IsNotExist(e) {
			t.Errorf("removed hosted path remains: %s", path)
		}
	}
	entries, e := os.ReadDir(filepath.Join(root, "cmd"))
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 1 || entries[0].Name() != "holark" {
		t.Fatalf("shipped commands = %v, want only holark", entries)
	}
}

func TestProductionDoesNotImportRemovedHostedPackages(t *testing.T) {
	root := repositoryRoot(t)
	for _, top := range []string{"cmd", "internal"} {
		e := filepath.WalkDir(filepath.Join(root, top), func(path string, entry os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			assertNoImports(t, path, "internal/controlplane", "internal/httpapi", "internal/store", "internal/nodeclient", "internal/iderelay", "internal/gitssh")
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
}

func TestProductionContainsNoHostedWireOrLegacyIdentity(t *testing.T) {
	root := repositoryRoot(t)
	for _, top := range []string{"cmd", "internal", "web/src"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/frontend/dist/") {
				return nil
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, forbidden := range []string{"PASTRAMI", "Pastrami", "pastrami", "configure_ide_relay", "DecodeNodeMessage"} {
				if strings.Contains(string(contents), forbidden) {
					t.Errorf("legacy identity or hosted wire token %q remains in %s", forbidden, path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
