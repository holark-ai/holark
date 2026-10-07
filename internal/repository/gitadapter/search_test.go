package gitadapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repositoryhttp"
)

func TestRepositorySearchHTTP(t *testing.T) {
	root, _ := fixture(t)
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"nested/Needle name.txt": "other text\n",
		"nested/content.txt":     "a NEEDLE in content\n--literal[.*]\n",
		"needle.bin":             "\x00needle\x00",
	}
	for i := 0; i < 201; i++ {
		files[fmt.Sprintf("nested/limit-%03d.txt", i)] = "limit query\n"
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "searchable files")
	git(t, root, "push", "origin", "main")
	git(t, root, "checkout", "-b", "other")
	git(t, root, "rm", "nested/content.txt")
	git(t, root, "commit", "-m", "remove content match")
	git(t, root, "push", "origin", "other")
	g, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	handler := repositoryhttp.New(repository.NewService(g))
	search := func(ref, query string, status int) repository.SearchResults {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/repository/search?"+url.Values{"ref": {ref}, "q": {query}}.Encode(), nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var result repository.SearchResults
		if status == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	got := search("main", "needle", http.StatusOK)
	want := []repository.SearchMatch{
		{Path: "needle.bin", NameMatch: true},
		{Path: "nested/Needle name.txt", NameMatch: true},
		{Path: "nested/content.txt", ContentMatch: true},
	}
	if !reflect.DeepEqual(got.Matches, want) {
		t.Fatalf("matches=%+v", got.Matches)
	}
	if got := search("other", "needle", http.StatusOK); len(got.Matches) != 2 {
		t.Fatalf("other matches=%+v", got.Matches)
	}
	if got := search("main", "--literal[.*]", http.StatusOK); len(got.Matches) != 1 || got.Matches[0].Path != "nested/content.txt" {
		t.Fatalf("literal matches=%+v", got.Matches)
	}
	if got := search("main", "no-such-match", http.StatusOK); len(got.Matches) != 0 || got.Matches == nil {
		t.Fatalf("empty matches=%+v", got)
	}
	if got := search("main", "limit query", http.StatusOK); len(got.Matches) != 200 || !got.Truncated {
		t.Fatalf("limit=%d truncated=%v", len(got.Matches), got.Truncated)
	}
	search("missing", "needle", http.StatusNotFound)
	search("main", "two\nlines", http.StatusBadRequest)
}
