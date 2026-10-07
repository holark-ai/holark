package localapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/holons"
	holonshttp "github.com/holark-ai/holark/internal/holons/httpapi"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

func TestWorkspaceEndpointContents(t *testing.T) {
	const original = " \toriginal café\r\n\r\n"
	const modified = "\tmodified 日本語\r\nno final newline  "
	const renamed = "rename keeps this unique content\nsecond line\n"
	path, handler, endpoint := workspaceContentsEndpoint(t, map[string]string{
		"modified.txt": original,
		"deleted.txt":  "deleted content\n\n",
		"old name.txt": renamed,
	})
	gitPlumbingWrite(t, path, "modified.txt", modified)
	gitPlumbingWrite(t, path, "added.txt", "added content\n")
	gitPlumbingWrite(t, path, "empty.txt", "")
	gitPlumbingCommand(t, path, "rm", "deleted.txt")
	gitPlumbingCommand(t, path, "mv", "old name.txt", "new name.txt")
	gitPlumbingCommand(t, path, "add", "added.txt", "empty.txt")
	gitPlumbingWrite(t, path, "untracked.txt", "untracked content\n\n")

	for _, target := range []string{"worktree", "commit"} {
		if target == "commit" {
			gitPlumbingCommand(t, path, "add", ".")
			gitPlumbingCommand(t, path, "commit", "-m", "content changes")
			target = "commit:" + gitPlumbingOutput(t, path, "rev-parse", "HEAD")
			// A commit comparison must read Git blobs, even with a dirty worktree.
			gitPlumbingWrite(t, path, "modified.txt", "later worktree edit\n")
		}
		t.Run(target, func(t *testing.T) {
			for _, tc := range []struct {
				path, oldPath, status, original, modified string
			}{
				{"modified.txt", "", "M", original, modified},
				{"added.txt", "", "A", "", "added content\n"},
				{"empty.txt", "", "A", "", ""},
				{"deleted.txt", "", "D", "deleted content\n\n", ""},
				{"new name.txt", "old name.txt", "R", renamed, renamed},
				{"untracked.txt", "", "A", "", "untracked content\n\n"},
			} {
				t.Run(tc.path, func(t *testing.T) {
					for _, reverse := range []bool{false, true} {
						baseRef, targetRef := "session-start", target
						wantPath, wantOldPath, wantStatus := tc.path, tc.oldPath, tc.status
						want := holons.WorkspaceContents{Original: tc.original, Modified: tc.modified}
						if reverse {
							baseRef, targetRef = targetRef, baseRef
							want.Original, want.Modified = want.Modified, want.Original
							if wantOldPath != "" {
								wantPath, wantOldPath = wantOldPath, wantPath
							}
							switch wantStatus {
							case "A":
								wantStatus = "D"
							case "D":
								wantStatus = "A"
							}
						}
						got := requestWorkspaceContents(t, handler, endpoint, baseRef, targetRef, wantPath)
						if got.Path != wantPath || got.OldPath != wantOldPath || !strings.HasPrefix(got.Status, wantStatus) {
							t.Fatalf("reverse=%v: file metadata = %+v", reverse, got)
						}
						if got.ContentStatus != "ready" || got.Binary || got.Contents == nil || *got.Contents != want {
							t.Fatalf("reverse=%v: status=%q binary=%v contents=%+v, want %+v", reverse, got.ContentStatus, got.Binary, got.Contents, want)
						}
					}
				})
			}
		})
	}
}

func TestWorkspaceEndpointContentSafeguards(t *testing.T) {
	const limit = 256 * 1024
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	gitPlumbingWrite(t, outside, "secret.txt", "outside contents must never be returned\n")
	path, handler, endpoint := workspaceContentsEndpoint(t, map[string]string{
		"nul.bin": "text before binary\n", "invalid-utf8.bin": "valid UTF-8\n",
		"oversized.txt": "small\n", "boundary.txt": "small\n",
	})
	gitPlumbingWrite(t, path, "nul.bin", "binary\x00data")
	gitPlumbingWrite(t, path, "invalid-utf8.bin", "invalid\xffdata")
	gitPlumbingWrite(t, path, "oversized.txt", strings.Repeat("x", limit+1))
	boundary := strings.Repeat("x", limit)
	gitPlumbingWrite(t, path, "boundary.txt", boundary)
	if err := os.Symlink(secret, filepath.Join(path, "external-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(path, "dangling-link")); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"worktree", "commit"} {
		if target == "commit" {
			gitPlumbingCommand(t, path, "add", ".")
			gitPlumbingCommand(t, path, "commit", "-m", "binary large files and symlinks")
			target = "commit:" + gitPlumbingOutput(t, path, "rev-parse", "HEAD")
		}
		t.Run(target, func(t *testing.T) {
			for _, tc := range []struct {
				path, status, original, modified string
				binary                           bool
			}{
				{"nul.bin", "binary", "", "", true},
				{"invalid-utf8.bin", "binary", "", "", true},
				{"oversized.txt", "too_large", "", "", false},
				{"boundary.txt", "ready", "small\n", boundary, false},
				{"external-link", "ready", "", secret, false},
				{"dangling-link", "ready", "", filepath.Join(outside, "missing"), false},
			} {
				t.Run(tc.path, func(t *testing.T) {
					for _, reverse := range []bool{false, true} {
						baseRef, targetRef := "session-start", target
						want := holons.WorkspaceContents{Original: tc.original, Modified: tc.modified}
						if reverse {
							baseRef, targetRef = targetRef, baseRef
							want.Original, want.Modified = want.Modified, want.Original
						}
						got := requestWorkspaceContents(t, handler, endpoint, baseRef, targetRef, tc.path)
						if got.ContentStatus != tc.status || got.Binary != tc.binary {
							t.Fatalf("reverse=%v: status=%q binary=%v, want %q binary=%v", reverse, got.ContentStatus, got.Binary, tc.status, tc.binary)
						}
						if tc.status == "ready" {
							if got.Contents == nil || *got.Contents != want {
								t.Fatalf("reverse=%v: exact contents differ for %s", reverse, tc.path)
							}
						} else if got.Contents != nil {
							t.Fatalf("reverse=%v: unavailable contents were returned for %s", reverse, tc.path)
						}
					}
				})
			}
		})
	}
}

func workspaceContentsEndpoint(t *testing.T, files map[string]string) (string, http.Handler, string) {
	t.Helper()
	source := t.TempDir()
	gitPlumbingCommand(t, source, "init", "-b", "main")
	gitPlumbingCommand(t, source, "config", "user.name", "Workspace Test")
	gitPlumbingCommand(t, source, "config", "user.email", "workspace@example.test")
	// Preserve fixture bytes regardless of the developer's Git configuration.
	gitPlumbingCommand(t, source, "config", "core.autocrlf", "false")
	for name, content := range files {
		gitPlumbingWrite(t, source, name, content)
	}
	gitPlumbingCommand(t, source, "add", ".")
	gitPlumbingCommand(t, source, "commit", "-m", "initial contents")
	base := gitPlumbingOutput(t, source, "rev-parse", "HEAD")
	git, err := gitadapter.OpenWithWorktrees(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, store := terminalTestService(t)
	service := holons.NewServiceWithRepository(store, holonRepositoryCoordinator{repository.NewService(git)})
	h, err := service.Create(t.Context(), holons.Create{Title: "Contents", BaseBranch: "main", BaseCommit: base})
	if err != nil {
		t.Fatal(err)
	}
	return h.WorktreePath, holonshttp.New(service), "/api/v1/holons/" + h.ID + "/workspace"
}

func requestWorkspaceContents(t *testing.T, handler http.Handler, endpoint, base, target, path string) holons.WorkspaceChange {
	t.Helper()
	query := url.Values{"contents": {"true"}, "base": {base}, "target": {target}, "path": {path}}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, endpoint+"?"+query.Encode(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("workspace %s: %d %s", query.Encode(), w.Code, w.Body.String())
	}
	var got holons.WorkspaceInspection
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != path {
		t.Fatalf("workspace %s: expected only %q, got %+v", query.Encode(), path, got.Files)
	}
	return got.Files[0]
}
