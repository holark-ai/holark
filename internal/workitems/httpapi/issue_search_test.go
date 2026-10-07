package httpapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/issues"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

func TestIssueSearchLabelsPeopleStatesAndPages(t *testing.T) {
	f := setup(t)
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	label := func(name string) issues.Label {
		return issues.Label{Name: name, Color: "abcdef", Description: "Label " + name, SyncProvider: "github", SyncExternalID: name}
	}
	inputs := []issues.Issue{}
	for i, names := range [][]string{{"bug", "frontend", "help wanted", "Équipe", `quote " and \\`}, {"bug", "backend"}, {"frontend", "backend"}, {"bug"}} {
		labels := []issues.Label{}
		for _, name := range names {
			labels = append(labels, label(name))
		}
		inputs = append(inputs, issues.Issue{Title: fmt.Sprintf("Fix quoted phrase %d", i+1), Status: issues.IssueOpen,
			SyncProvider: "github", SyncExternalID: fmt.Sprintf("issue-%d", i+1),
			SyncData:       json.RawMessage(fmt.Sprintf(`{"github":{"number":%d,"url":"https://github.com/example/repo/issues/%d"}}`, i+1, i+1)),
			IssuerHolarkID: f.me, AssigneeHolarkIDs: []string{f.me}, Labels: labels, CreatedAt: at.Add(time.Duration(i) * time.Hour), UpdatedAt: at.Add(time.Duration(i) * time.Hour)})
	}
	inputs[3].Status = issues.IssueClosed
	if _, _, err := f.issues.UpsertSynced(t.Context(), "repo", inputs); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.issues.UpsertSynced(t.Context(), "other", []issues.Issue{{Title: "Elsewhere", Status: issues.IssueOpen, SyncProvider: "github", SyncExternalID: "other", Labels: []issues.Label{label("private-label")}, CreatedAt: at, UpdatedAt: at}}); err != nil {
		t.Fatal(err)
	}
	local, err := f.issues.Create(t.Context(), "repo", "Local issue", "")
	if err != nil {
		t.Fatal(err)
	}
	f.add(t, "pr-1", "repo", pr.StatusOpen, 1)
	first := f.get(t, "/api/v1/issues/search?q=%231").Rows[0]
	if _, err := f.db.Exec(`insert into issue_pull_requests(issue_id,pull_request_id) values(?,?)`, first.ID, "pr-1"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		query   string
		numbers []int
	}{
		{"", []int{0, 3, 2, 1}}, {"is:closed", []int{4}}, {"state:none", []int{}}, {"state:open,closed sort:created-asc", []int{1, 2, 3, 4, 0}},
		{`#1`, []int{1}}, {`1`, []int{1}}, {`"quoted phrase" in:title author:ALICE assignee:@me sort:updated-asc`, []int{1, 2, 3}},
		{`label:bug`, []int{2, 1}}, {`label:BUG label:frontend`, []int{1}}, {`label:(bug OR frontend)`, []int{3, 2, 1}},
		{`label:(bug AND (frontend OR backend))`, []int{2, 1}},
		{`label:(bug OR frontend AND backend) is:all sort:created-asc`, []int{1, 2, 3, 4}},
		{`label:((bug OR frontend) AND backend)`, []int{3, 2}},
		{`label:"help wanted"`, []int{1}}, {`label:"quote \" and \\\\"`, []int{1}},
		{`label:éQUIPE`, []int{1}}, {`label:missing`, []int{}}, {`label:bu`, []int{}}, {`label:(bug OR bug)`, []int{2, 1}},
		{`label:bug is:closed`, []int{4}}, {`label:bug author:missing`, []int{}},
		{strings.Repeat("label:bug ", 100), []int{2, 1}},
		{strings.Repeat("label:(bug AND (bug OR frontend)) ", 33) + "label:bug", []int{2, 1}},
		{strings.Repeat("label:(bug"+strings.Repeat(" ", 4091)+") ", 2), []int{2, 1}},
		{strings.Repeat("label:"+strings.Repeat("a", 4094)+" ", 2), []int{}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			result := f.get(t, "/api/v1/issues/search?q="+url.QueryEscape(tc.query))
			numbers := []int{}
			for _, row := range result.Rows {
				numbers = append(numbers, row.Number)
				if row.Kind != "issue" {
					t.Fatal(row)
				}
			}
			if !reflect.DeepEqual(numbers, tc.numbers) || result.Total != len(tc.numbers) {
				t.Fatalf("query %q: numbers %v total %d, want %v", tc.query, numbers, result.Total, tc.numbers)
			}
			// Canonical queries remain valid and preserve the complete expression.
			again := f.get(t, "/api/v1/issues/search?q="+url.QueryEscape(result.Query))
			if !reflect.DeepEqual(result.Rows, again.Rows) || result.Query != again.Query {
				t.Fatalf("canonical query changed results: %q", result.Query)
			}
		})
	}
	if result := f.get(t, "/api/v1/issues/search?q=Local"); len(result.Rows) != 1 || result.Rows[0].ID != local.ID {
		t.Fatal(result)
	}
	first = f.get(t, "/api/v1/issues/search?q=%231").Rows[0]
	if len(first.Labels) != 5 || first.URL != "https://github.com/example/repo/issues/1" || first.AuthorID != f.me || !reflect.DeepEqual(first.LinkedPullRequestIDs, []string{"pr-1"}) {
		t.Fatal(first)
	}
	for page, want := range []int{1, 2, 3} {
		result := f.get(t, fmt.Sprintf("/api/v1/issues/search?q=%s&per_page=1&page=%d", url.QueryEscape("label:(bug OR frontend) sort:created-asc"), page+1))
		if result.Total != 3 || len(result.Rows) != 1 || result.Rows[0].Number != want {
			t.Fatal(result)
		}
	}
	// Both filters run before counting and pagination, including nested groups.
	for page, want := range []int{1, 2} {
		result := f.get(t, fmt.Sprintf("/api/v1/issues/search?q=%s&title=pharse&per_page=1&page=%d", url.QueryEscape("label:(bug AND (frontend OR backend)) sort:created-asc"), page+1))
		if result.Total != 2 || len(result.Rows) != 1 || result.Rows[0].Number != want {
			t.Fatalf("combined search page %d: %+v", page+1, result)
		}
	}
	if result := f.get(t, "/api/v1/issues/search?q=label:bug&title=Local"); result.Total != 0 || len(result.Rows) != 0 {
		t.Fatalf("title must also match: %+v", result)
	}

	response := httptest.NewRecorder()
	f.mux.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/issues/search/labels", nil))
	var labels []issues.Label
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &labels) != nil || len(labels) != 6 {
		t.Fatalf("labels: %d %s", response.Code, response.Body.String())
	}
	for _, label := range labels {
		if label.Name == "private-label" {
			t.Fatal("leaked another repository's label")
		}
	}
	if err := f.work.RecordSync(t.Context(), "repo", "issues", errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	cached := f.get(t, "/api/v1/issues/search?q=label:bug")
	if cached.Total != 2 || len(cached.Sync) != 1 || cached.Sync[0].Error != "offline" {
		t.Fatal(cached)
	}
	if f.sync.calls != 0 {
		t.Fatal("search triggered synchronization")
	}
}

func TestIssueSearchRejectsInvalidLabelExpressionsAndPRFilters(t *testing.T) {
	f := setup(t)
	// Invalid searches must be rejected before accessing the database.
	f.db.Close()
	for _, query := range []string{
		"draft:true", "user-review-requested:@me", "is:merged", "is:active", "state:draft", "author:a author:b",
		"label:", "label:()", "label:(bug OR)", "label:(AND bug)", "label:(bug frontend)", "label:(bug XOR frontend)", "label:(bug", "label:bug)",
		`label:"unfinished`, `label:""`, `label:" "`, `label:"bug"extra`, `label:(bug "frontend")`, `label:NOT`,
		"label:" + strings.Repeat("(", 9) + "bug" + strings.Repeat(")", 9), "label:(" + strings.Repeat("bug OR ", 100) + "bug)",
		"label:" + strings.Repeat("a", 8193), "label:bug OR label:frontend", "label:bug AND label:frontend",
		strings.Repeat("label:bug ", 101), strings.Repeat("label:bug ", 1000),
		strings.Repeat("label:(bug AND (bug OR frontend)) ", 33) + "label:(bug OR frontend)",
		strings.Repeat("label:(bug"+strings.Repeat(" ", 4091)+") ", 2) + "label:bug",
		strings.Repeat("label:"+strings.Repeat("a", 4094)+" ", 2) + "label:a",
		strings.Repeat("label:"+strings.Repeat("<", 683)+" ", 2),
	} {
		response := httptest.NewRecorder()
		f.mux.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/issues/search?q="+url.QueryEscape(query), nil))
		if response.Code != 400 {
			t.Fatalf("accepted %q: %d %s", query, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/issues/search", "/api/v1/issues/search/labels"} {
		response := httptest.NewRecorder()
		f.mux.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 500 {
			t.Fatalf("database failure became success: %s", path)
		}
	}
}
