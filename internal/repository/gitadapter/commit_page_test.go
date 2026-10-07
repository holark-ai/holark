package gitadapter

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repositoryhttp"
)

func TestCommitHistoryPaginationHTTP(t *testing.T) {
	root, _ := fixture(t)
	git(t, root, "checkout", "-b", "side")
	git(t, root, "commit", "--allow-empty", "-m", "side one")
	git(t, root, "commit", "--allow-empty", "-m", "side two")
	git(t, root, "checkout", "main")
	git(t, root, "commit", "--allow-empty", "-m", "main one")
	git(t, root, "merge", "--no-ff", "side", "-m", "merge side")
	git(t, root, "commit", "--allow-empty", "-m", "main two")
	git(t, root, "push", "origin", "main")
	want := strings.Fields(git(t, root, "log", "--format=%H"))
	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	handler := repositoryhttp.New(repository.NewService(adapter))
	request := func(cursor string, status int) repository.CommitPage {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/repository/commits?"+url.Values{"ref": {"main"}, "limit": {"2"}, "cursor": {cursor}}.Encode(), nil))
		if response.Code != status {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		var page repository.CommitPage
		if status == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
		}
		return page
	}
	page := request("", http.StatusOK)
	if len(page.Commits) != 2 || page.NextCursor == "" || page.Head != want[0] {
		t.Fatalf("first page: %+v", page)
	}
	// Move the branch while the reader is paging through its previous snapshot.
	git(t, root, "commit", "--allow-empty", "-m", "new arrival")
	git(t, root, "push", "origin", "main")
	if _, err = adapter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got []string
	for attempts := 0; ; attempts++ {
		if attempts > 5 {
			t.Fatal("pagination did not terminate")
		}
		if page.Head != want[0] {
			t.Fatalf("snapshot moved: %+v", page)
		}
		for _, commit := range page.Commits {
			got = append(got, commit.SHA)
		}
		if page.NextCursor == "" {
			break
		}
		page = request(page.NextCursor, http.StatusOK)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history=%v, want %v", got, want)
	}
	if next := request("", http.StatusOK); next.Head == want[0] {
		t.Fatal("fresh history did not include new arrival")
	}
	request("not-a-cursor", http.StatusBadRequest)
	request(base64.RawURLEncoding.EncodeToString([]byte(`{"head":"--all","offset":2}`)), http.StatusBadRequest)
}
