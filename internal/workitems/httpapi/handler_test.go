package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/githubidentity"
	membersql "github.com/holark-ai/holark/internal/githubidentity/storeadapter"
	"github.com/holark-ai/holark/internal/issues"
	issuesql "github.com/holark-ai/holark/internal/issues/storeadapter"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prhttp "github.com/holark-ai/holark/internal/pullrequestlifecycle/httpapi"
	prsql "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
	participantsql "github.com/holark-ai/holark/internal/pullrequestparticipants/sqliteadapter"
	"github.com/holark-ai/holark/internal/workitems"
	"github.com/holark-ai/holark/internal/workitems/httpapi"
	worksql "github.com/holark-ai/holark/internal/workitems/sqliteadapter"
)

type syncCounter struct {
	calls int
	run   func(context.Context) error
}

func (s *syncCounter) Sync(ctx context.Context) error {
	s.calls++
	if s.run != nil {
		return s.run(ctx)
	}
	return nil
}

func TestMyWorkSyncWaitsForCompletion(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%t", failed), func(t *testing.T) {
			f := setup(t)
			started := make(chan struct{})
			finish := make(chan error, 1)
			f.sync.run = func(ctx context.Context) error {
				close(started)
				select {
				case err := <-finish:
					return err
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				f.mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/my-work/sync", nil).WithContext(t.Context()))
			}()
			<-started
			select {
			case <-done:
				t.Fatal("sync response returned before synchronization finished")
			case <-time.After(50 * time.Millisecond):
			}
			if failed {
				finish <- errors.New("provider failed")
			} else {
				finish <- nil
			}
			<-done
			if failed {
				if response.Code != http.StatusBadGateway {
					t.Fatalf("failed sync: %d %s", response.Code, response.Body.String())
				}
			} else if response.Code != http.StatusOK || response.Body.String() != "{\"synced\":true}\n" {
				t.Fatalf("completed sync: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMyWorkSyncReportsGitHubRateLimit(t *testing.T) {
	f := setup(t)
	f.sync.run = func(context.Context) error {
		return &pr.GitHubError{RateLimitExceeded: true, Err: errors.New("limited")}
	}
	response := httptest.NewRecorder()
	f.mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/my-work/sync", nil))
	const want = "{\"message\":\"GitHub API rate limit exceeded. Try again after the limit resets.\"}\n"
	if response.Code != http.StatusTooManyRequests || response.Body.String() != want {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

type fixture struct {
	db           *sql.DB
	mux          *http.ServeMux
	catalog      *prsql.Store
	participants *participantsql.Store
	members      *membersql.Store
	work         *worksql.Store
	issues       *issues.Service
	sync         *syncCounter
	me           string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "work.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`create table repositories(id text primary key);insert into repositories values('repo'),('other')`); err != nil {
		t.Fatal(err)
	}
	catalog, err := prsql.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	participants, err := participantsql.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	members, err := membersql.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	issueStore, err := issuesql.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	work, err := worksql.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	me, err := githubidentity.NewService(members, nil).ObserveProjectMember(ctx, "repo", githubidentity.SourceMember{NodeID: "U_me", Login: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err = members.SetAuthenticatedMember(ctx, "repo", me); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	sync := &syncCounter{}
	register := func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }
	httpapi.RegisterRoutes(register, workitems.New(work, "repo"), sync)
	prhttp.RegisterRoutes(register, prhttp.Options{RepositoryID: "repo", Catalog: catalog, Participants: pullrequestparticipants.NewService(participants, nil, nil, nil)})
	return &fixture{db, mux, catalog, participants, members, work, issues.NewService(issueStore), sync, me}
}
func (f *fixture) add(t *testing.T, id, repo string, status pr.Status, number int) {
	t.Helper()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	_, err := f.catalog.CreatePullRequest(pr.PullRequest{ID: id, RepositoryID: repo, Title: "Fix quoted phrase " + id, Status: status, CreatedAt: at, UpdatedAt: at, SyncData: json.RawMessage(fmt.Sprintf(`{"github":{"number":%d}}`, number))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.participants.ReplaceSnapshot(t.Context(), pullrequestparticipants.Snapshot{PullRequestID: id, AuthorHolarkID: f.me, AssigneeHolarkIDs: []string{f.me}, RequestedReviewerHolarkIDs: []string{f.me}}); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) get(t *testing.T, path string) workitems.Result {
	t.Helper()
	response := httptest.NewRecorder()
	f.mux.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
	if response.Code != 200 {
		t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
	}
	var result workitems.Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestSearchLifecyclePeoplePaginationAndLegacyEndpoint(t *testing.T) {
	f := setup(t)
	for i, status := range []pr.Status{pr.StatusWIP, pr.StatusDraft, pr.StatusOpen, pr.StatusClosed, pr.StatusMerged} {
		f.add(t, fmt.Sprintf("pr-%d", i), "repo", status, 120+i)
	}
	f.add(t, "elsewhere", "other", pr.StatusOpen, 122)
	// The mirrored legacy table must not decide lifecycle semantics.
	if _, err := f.db.Exec(`update pull_requests set status='closed' where id='pr-2'`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		query string
		want  int
	}{
		{"", 3}, {"is:open", 2}, {"is:closed", 2}, {"is:closed is:unmerged", 1}, {"is:merged", 1}, {"is:wip", 1}, {"is:all", 5},
		{"is:all draft:true", 1}, {"is:all draft:false", 4}, {"#122", 1}, {"122", 1}, {`fix "quoted phrase" in:title author:alice assignee:@me user-review-requested:ALICE is:open`, 2},
		{"author:missing", 0}, {`"not here"`, 0}, {"is:open is:merged", 0}, {"is:all sort:updated-asc", 5},
		{"state:open,merged", 2}, {"state:closed", 1}, {"state:draft", 1}, {"state:wip,draft,open,closed,merged", 5}, {"state:none", 0},
		{"state:open,merged author:missing", 0}, {"state:open,merged is:unmerged", 1}, {"state:draft,open draft:false", 1},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			result := f.get(t, "/api/v1/pull-requests/search?q="+url.QueryEscape(tc.query))
			if result.Total != tc.want || len(result.Rows) != tc.want {
				t.Fatalf("%q: %+v", tc.query, result)
			}
		})
	}
	for page, want := range []string{"pr-0", "pr-1", "pr-2"} {
		r := f.get(t, fmt.Sprintf("/api/v1/pull-requests/search?page=%d&per_page=1", page+1))
		if r.Total != 3 || r.Rows[0].ID != want {
			t.Fatalf("unstable page: %+v", r)
		}
	}
	if r := f.get(t, "/api/v1/pull-requests/search?per_page=1000"); r.PerPage != 100 {
		t.Fatal(r.PerPage)
	}
	response := httptest.NewRecorder()
	f.mux.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/pull-requests", nil))
	var legacy []json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &legacy) != nil || len(legacy) != 5 {
		t.Fatalf("legacy endpoint changed: %s", response.Body.String())
	}
	if _, err := f.db.Exec(`update pull_request_catalog set document=json_set(document,'$.sync_data.github.draft',json('true')) where id='pr-3'`); err != nil {
		t.Fatal(err)
	}
	if r := f.get(t, "/api/v1/pull-requests/search?q="+url.QueryEscape("is:closed draft:true")); r.Total != 1 || r.Rows[0].ID != "pr-3" {
		t.Fatal(r)
	}
	if f.sync.calls != 0 {
		t.Fatal("read scheduled synchronization")
	}
}
func TestTimestampSortingAndPagination(t *testing.T) {
	f := setup(t)
	base := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	half := base.Add(500 * time.Millisecond)
	newer := half.Add(time.Nanosecond)
	for _, item := range []struct {
		id               string
		created, updated time.Time
	}{
		{"a", base, newer},
		{"b", half, half},
		{"c", half.In(time.FixedZone("east", 2*60*60)), half.In(time.FixedZone("east", 2*60*60))},
		{"d", newer.In(time.FixedZone("west", -5*60*60)), base},
	} {
		f.add(t, item.id, "repo", pr.StatusOpen, 1)
		if _, err := f.db.Exec(`update pull_request_catalog set document=json_set(document,'$.created_at',?,'$.updated_at',?) where id=?`, item.created.Format(time.RFC3339Nano), item.updated.Format(time.RFC3339Nano), item.id); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		sort string
		want []string
	}{
		{"created-desc", []string{"d", "b", "c", "a"}},
		{"created-asc", []string{"a", "b", "c", "d"}},
		{"updated-desc", []string{"a", "b", "c", "d"}},
		{"updated-asc", []string{"d", "b", "c", "a"}},
	} {
		t.Run(tc.sort, func(t *testing.T) {
			for page, id := range tc.want {
				r := f.get(t, fmt.Sprintf("/api/v1/pull-requests/search?q=sort:%s&page=%d&per_page=1", tc.sort, page+1))
				if r.Total != len(tc.want) || len(r.Rows) != 1 || r.Rows[0].ID != id {
					t.Fatalf("page %d: want %s, got %+v", page+1, id, r)
				}
			}
		})
	}
	_, _, err := f.issues.UpsertSynced(t.Context(), "repo", []issues.Issue{{Title: "Newest assigned issue", Status: issues.IssueOpen, SyncProvider: "github", SyncExternalID: "issue-1", AssigneeHolarkIDs: []string{f.me}, CreatedAt: base, UpdatedAt: newer.Add(time.Nanosecond)}})
	if err != nil {
		t.Fatal(err)
	}
	issue := f.get(t, "/api/v1/my-work?view=assigned_issues")
	if len(issue.Rows) != 1 {
		t.Fatalf("want one assigned issue, got %+v", issue)
	}
	for page, id := range []string{issue.Rows[0].ID, "a", "b", "c", "d"} {
		r := f.get(t, fmt.Sprintf("/api/v1/my-work?page=%d&per_page=1", page+1))
		if r.Total != 5 || len(r.Rows) != 1 || r.Rows[0].ID != id {
			t.Fatalf("My work page %d: want %s, got %+v", page+1, id, r)
		}
	}
}

func TestSearchUnicodeTitleCanonicalization(t *testing.T) {
	f := setup(t)
	f.add(t, "unicode-title", "repo", pr.StatusOpen, 123)
	if err := f.catalog.UpdatePullRequestMetadata("unicode-title", "mise à jour", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query     string
		canonical string
	}{
		{"mise à jour", "mise à jour"},
		{"\u2003mise\u00a0à\u2003jour\u00a0", "mise à jour"},
		{"\"mise\"\u2003à jour", "\"mise\" à jour"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			result := f.get(t, "/api/v1/pull-requests/search?q="+url.QueryEscape(tc.query))
			want := tc.canonical + " is:active sort:created-desc"
			if result.Query != want {
				t.Fatalf("canonical query = %q, want %q", result.Query, want)
			}
			if result.Total != 1 || len(result.Rows) != 1 || result.Rows[0].ID != "unicode-title" {
				t.Fatalf("title did not match: %+v", result)
			}
			canonical := f.get(t, "/api/v1/pull-requests/search?q="+url.QueryEscape(result.Query))
			if !reflect.DeepEqual(canonical, result) {
				t.Fatalf("canonical query changed search results: %+v", canonical)
			}
		})
	}
}

func TestMyWorkCountsRemovalMissingIdentityAndFailureCache(t *testing.T) {
	f := setup(t)
	for i, status := range []pr.Status{pr.StatusWIP, pr.StatusDraft, pr.StatusOpen, pr.StatusClosed, pr.StatusMerged} {
		f.add(t, fmt.Sprintf("pr-%d", i), "repo", status, i+1)
	}
	at := time.Now().UTC()
	_, _, err := f.issues.UpsertSynced(t.Context(), "repo", []issues.Issue{{Title: "Assigned issue", Status: issues.IssueOpen, SyncProvider: "github", SyncExternalID: "issue-1", AssigneeHolarkIDs: []string{f.me}, CreatedAt: at, UpdatedAt: at}, {Title: "Closed issue", Status: issues.IssueClosed, SyncProvider: "github", SyncExternalID: "issue-2", AssigneeHolarkIDs: []string{f.me}, CreatedAt: at, UpdatedAt: at}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"all": 4, "review_requested": 2, "assigned_issues": 1, "assigned_pull_requests": 3}
	for view, count := range want {
		r := f.get(t, "/api/v1/my-work?view="+view)
		if r.Total != count || len(r.Rows) != count || !reflect.DeepEqual(r.Counts, want) {
			t.Fatalf("view %s: %+v", view, r)
		}
		if view == "all" {
			for _, row := range r.Rows {
				if row.ID == "pr-2" && len(row.Reasons) != 2 {
					t.Fatal(row)
				}
			}
		}
	}
	if err = f.work.RecordSync(t.Context(), "repo", "issues", nil); err != nil {
		t.Fatal(err)
	}
	if err = f.work.RecordSync(t.Context(), "repo", "issues", errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	r := f.get(t, "/api/v1/my-work")
	if r.Total != 4 || r.Sync[0].Error != "offline" || r.Sync[0].SyncedAt == "" {
		t.Fatal(r)
	}
	if _, err = f.participants.ReplaceSnapshot(t.Context(), pullrequestparticipants.EmptySnapshot("pr-2")); err != nil {
		t.Fatal(err)
	}
	r = f.get(t, "/api/v1/my-work")
	if r.Counts["all"] != 3 || r.Counts["review_requested"] != 1 || r.Counts["assigned_pull_requests"] != 2 {
		t.Fatal(r)
	}
	if _, err = f.db.Exec(`delete from issue_assignees where holark_id=?`, f.me); err != nil {
		t.Fatal(err)
	}
	r = f.get(t, "/api/v1/my-work")
	if r.Counts["assigned_issues"] != 0 || r.Total != 2 {
		t.Fatal(r)
	}
	if _, err = f.db.Exec(`update github_repository_members set is_me=0`); err != nil {
		t.Fatal(err)
	}
	r = f.get(t, "/api/v1/my-work")
	if r.Identity != nil || r.Total != 0 || r.Counts["all"] != 0 {
		t.Fatal(r)
	}
	if f.sync.calls != 0 {
		t.Fatal("read caused sync")
	}
}
func TestInvalidQueriesAndDatabaseFailuresAreExplicit(t *testing.T) {
	f := setup(t)
	for _, q := range []string{"label:bug", "author:a author:b", "a OR b", "NOT bug", "(a)", `"unfinished`, "is:unknown", "state:unknown", "state:open,", "state:none,open", "state:open state:merged", "draft:maybe", "sort:random", "#bad", "author:", "author:[bot]", "author:dependabot[other]", "author:dependabot[bot]extra", "author:dependa[bot]bot", "in:body", "sort:created-desc sort:updated-desc"} {
		response := httptest.NewRecorder()
		f.mux.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/pull-requests/search?q="+url.QueryEscape(q), nil))
		if response.Code != 400 {
			t.Fatalf("accepted %q: %d", q, response.Code)
		}
	}
	f.db.Close()
	for _, path := range []string{"/api/v1/my-work", "/api/v1/pull-requests/search"} {
		response := httptest.NewRecorder()
		f.mux.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 500 {
			t.Fatalf("database error became success: %d", response.Code)
		}
	}
}

func TestFuzzyTitleSearch(t *testing.T) {
	f := setup(t)
	for _, item := range []struct{ id, title string }{
		{"navigation", "Improve workspace navigation"},
		{"literal", "Handle label:bug and #123 in titles"},
		{"unicode", "Améliorer les paramètres"},
		{"short", "Fix API timeout"},
	} {
		_, err := f.catalog.CreatePullRequest(pr.PullRequest{ID: item.id, RepositoryID: "repo", Title: item.title, Status: pr.StatusOpen})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ search, want string }{
		{"WORKSPACE navig", "navigation"},
		{"workspce", "navigation"},
		{"workspaace", "navigation"},
		{"workspxce", "navigation"},
		{"navigtaion", "navigation"},
		{"label:bug #123", "literal"},
		{"PARAMÈTRES", "unicode"},
		{"paramèters", "unicode"},
		{"api", "short"},
		{"apx", ""},
		{"wxxkspace", ""},
		{"workspace timeout", ""},
		{"author:@me", ""},
	} {
		t.Run(tc.search, func(t *testing.T) {
			r := f.get(t, "/api/v1/pull-requests/search?title="+url.QueryEscape(tc.search))
			if tc.want == "" {
				if r.Total != 0 || len(r.Rows) != 0 {
					t.Fatalf("expected empty search result: %+v", r)
				}
			} else if r.Total != 1 || len(r.Rows) != 1 || r.Rows[0].ID != tc.want {
				t.Fatalf("expected %s: %+v", tc.want, r)
			}
		})
	}
}

func TestFuzzyTitleSearchFiltersBeforePagination(t *testing.T) {
	f := setup(t)
	for i, item := range []struct {
		id, repo, title string
		status          pr.Status
	}{
		{"unrelated", "repo", "Update documentation", pr.StatusOpen},
		{"older", "repo", "Fix workspace navigation", pr.StatusOpen},
		{"newer", "repo", "Improve workspace navigation", pr.StatusOpen},
		{"closed", "repo", "Workspace navigation", pr.StatusClosed},
		{"elsewhere", "other", "Workspace navigation", pr.StatusOpen},
	} {
		at := time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)
		_, err := f.catalog.CreatePullRequest(pr.PullRequest{ID: item.id, RepositoryID: item.repo, Title: item.title, Status: item.status, CreatedAt: at, UpdatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.participants.ReplaceSnapshot(t.Context(), pullrequestparticipants.Snapshot{PullRequestID: item.id, AuthorHolarkID: f.me}); err != nil {
			t.Fatal(err)
		}
	}
	for _, sort := range []string{"created-asc", "created-desc"} {
		want := []string{"older", "newer"}
		if sort == "created-desc" {
			want = []string{"newer", "older"}
		}
		for page, id := range want {
			params := url.Values{"q": {"state:open author:@me sort:" + sort}, "title": {"workspce"}, "page": {fmt.Sprint(page + 1)}, "per_page": {"1"}}
			r := f.get(t, "/api/v1/pull-requests/search?"+params.Encode())
			if r.Total != 2 || r.Page != page+1 || r.PerPage != 1 || len(r.Rows) != 1 || r.Rows[0].ID != id {
				t.Fatalf("%s page %d: %+v", sort, page+1, r)
			}
		}
	}
	r := f.get(t, "/api/v1/pull-requests/search?q=state:open&title=workspce&page=3&per_page=1")
	if r.Total != 2 || len(r.Rows) != 0 {
		t.Fatalf("past last page: %+v", r)
	}
	r = f.get(t, "/api/v1/pull-requests/search?q=state:open+author:missing&title=workspce")
	if r.Total != 0 || len(r.Rows) != 0 {
		t.Fatalf("author filter: %+v", r)
	}
}

func TestIssueSearchFiltersPaginationAndMetadata(t *testing.T) {
	f := setup(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.add(t, "linked-pr", "repo", pr.StatusOpen, 99)
	_, _, err := f.issues.UpsertSynced(t.Context(), "repo", []issues.Issue{
		{Title: "Fix navigation", Status: issues.IssueOpen, SyncProvider: "github", SyncExternalID: "issue-1", SyncData: json.RawMessage(`{"github":{"number":42,"url":"https://github.com/example/repo/issues/42"}}`), IssuerHolarkID: f.me, AssigneeHolarkIDs: []string{f.me}, Labels: []issues.Label{{Name: "bug", Color: "d73a4a", Description: "Broken behavior", SyncProvider: "github", SyncExternalID: "bug"}}, CreatedAt: at, UpdatedAt: at},
		{Title: "Improve navigation", Status: issues.IssueOpen, SyncProvider: "github", SyncExternalID: "issue-2", CreatedAt: at.Add(time.Hour), UpdatedAt: at.Add(time.Hour)},
		{Title: "Closed navigation", Status: issues.IssueClosed, SyncProvider: "github", SyncExternalID: "issue-3", CreatedAt: at.Add(2 * time.Hour), UpdatedAt: at.Add(2 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.issues.UpsertSynced(t.Context(), "other", []issues.Issue{{Title: "Other navigation", Status: issues.IssueOpen, SyncProvider: "github", SyncExternalID: "other", CreatedAt: at, UpdatedAt: at}})
	if err != nil {
		t.Fatal(err)
	}
	first := f.get(t, "/api/v1/issues/search?per_page=1&title=navigaton")
	if first.Total != 2 || len(first.Rows) != 1 || first.Rows[0].Title != "Improve navigation" {
		t.Fatalf("first page: %+v", first)
	}
	second := f.get(t, "/api/v1/issues/search?per_page=1&page=2&title=navigaton")
	if second.Total != 2 || len(second.Rows) != 1 || second.Rows[0].Title != "Fix navigation" {
		t.Fatalf("second page: %+v", second)
	}
	if _, err = f.db.Exec(`insert into issue_pull_requests(issue_id,pull_request_id) values(?,?)`, second.Rows[0].ID, "linked-pr"); err != nil {
		t.Fatal(err)
	}
	filtered := f.get(t, "/api/v1/issues/search?q="+url.QueryEscape("is:open author:@me assignee:alice sort:updated-asc")+"&title=navigaton")
	if filtered.Total != 1 || len(filtered.Rows) != 1 {
		t.Fatalf("filtered: %+v", filtered)
	}
	row := filtered.Rows[0]
	if row.Kind != "issue" || row.Number != 42 || row.AuthorID != f.me || !reflect.DeepEqual(row.AssigneeIDs, []string{f.me}) {
		t.Fatalf("identity: %+v", row)
	}
	if row.URL != "https://github.com/example/repo/issues/42" || len(row.LinkedPullRequestIDs) != 1 || len(row.Labels) != 1 || row.Labels[0].Name != "bug" {
		t.Fatalf("metadata: %+v", row)
	}
	closed := f.get(t, "/api/v1/issues/search?q=state:closed")
	if closed.Total != 1 || closed.Rows[0].Title != "Closed navigation" {
		t.Fatalf("closed: %+v", closed)
	}
	all := f.get(t, "/api/v1/issues/search?q="+url.QueryEscape("is:all sort:created-asc"))
	if all.Total != 3 || all.Rows[0].Title != "Fix navigation" || all.Rows[2].Title != "Closed navigation" {
		t.Fatalf("all: %+v", all)
	}
	for _, query := range []string{"state:draft", "is:merged", "draft:true", "user-review-requested:@me"} {
		response := httptest.NewRecorder()
		f.mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/issues/search?q="+url.QueryEscape(query), nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", query, response.Code)
		}
	}
}
