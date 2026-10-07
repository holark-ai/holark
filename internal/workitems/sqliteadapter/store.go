// Package sqliteadapter reads the existing canonical catalog and collaboration projections.
package sqliteadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/githubidentity"
	"github.com/holark-ai/holark/internal/issues"
	"github.com/holark-ai/holark/internal/workitems"
)

type Store struct{ db *sql.DB }

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	err := database.CreateSchema(ctx, db, `
create table if not exists work_sync_state(repository_id text not null, section text not null, attempted_at text not null, synced_at text not null default '', error text not null default '', primary key(repository_id,section));
create index if not exists pull_request_catalog_browse on pull_request_catalog(repository_id,json_extract(document,'$.status'),json_extract(document,'$.created_at'),id);
create index if not exists pull_request_assignees_member on pull_request_assignees(holark_id,pull_request_id);
create index if not exists pull_request_reviewers_member on pull_request_requested_reviewers(holark_id,pull_request_id);
create index if not exists issue_assignees_member on issue_assignees(holark_id,issue_id);`)
	return &Store{db}, err
}
func (s *Store) RecordSync(ctx context.Context, repo, section string, syncErr error) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	success, message := "", ""
	if syncErr == nil {
		success = now
	} else {
		message = syncErr.Error()
	}
	_, err := s.db.ExecContext(ctx, `insert into work_sync_state(repository_id,section,attempted_at,synced_at,error) values(?,?,?,?,?) on conflict(repository_id,section) do update set attempted_at=excluded.attempted_at,synced_at=case when excluded.error='' then excluded.synced_at else work_sync_state.synced_at end,error=excluded.error`, repo, section, now, success, message)
	return err
}

const prCTE = `select p.id, p.repository_id, 'pull_request' kind,
 json_extract(p.document,'$.title') title, json_extract(p.document,'$.status') status,
 case json_extract(p.document,'$.status') when 'draft' then 1 when 'open' then 0 when 'wip' then 0 when 'merged' then 0 else coalesce(json_extract(p.document,'$.sync_data.github.draft'),0) end draft,
 coalesce(json_extract(p.document,'$.sync_data.github.number'),0) number,
 coalesce((select holark_id from pull_request_authors where pull_request_id=p.id),'') author_id,
 coalesce((select json_group_array(holark_id) from pull_request_assignees where pull_request_id=p.id),'[]') assignees,
 coalesce((select json_group_array(holark_id) from pull_request_requested_reviewers where pull_request_id=p.id),'[]') reviewers,
 '[]' labels, '[]' linked_pull_request_ids, '' url,
 json_extract(p.document,'$.created_at') created_at, json_extract(p.document,'$.updated_at') updated_at
 from pull_request_catalog p`
const issueCTE = `select i.id,i.repository_id,'issue' kind,i.title,i.status,0 draft,coalesce(json_extract(i.sync_data,'$.github.number'),0) number,i.issuer_holark_id author_id,
 coalesce((select json_group_array(holark_id) from issue_assignees where issue_id=i.id),'[]') assignees,'[]' reviewers,
 (select json_group_array(json_object('id',l.id,'name',l.name,'color',l.color,'description',l.description)) from (select l.* from issue_label_assignments a join issue_labels l on l.id=a.label_id where a.issue_id=i.id order by a.position) l) labels,
 (select json_group_array(pull_request_id) from issue_pull_requests where issue_id=i.id) linked_pull_request_ids,
 coalesce(json_extract(i.sync_data,'$.github.url'),'') url,
 i.created_at,i.updated_at from issues i`

func (s *Store) Search(ctx context.Context, repo string, q workitems.Query, page, size int) (workitems.Result, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workitems.Result{}, err
	}
	defer tx.Rollback()
	result, err := metadata(ctx, tx, repo, page, size)
	if err != nil {
		return result, err
	}
	result.Query = q.Canonical
	cte := prCTE
	if q.Issues {
		cte = issueCTE
		result.Sync = sections(result.Sync, "identity", "issues")
	} else {
		result.Sync = sections(result.Sync, "identity", "pull_requests", "history", "pull_requests_participants", "history_participants")
	}
	predicates := []string{"repository_id=?"}
	args := []any{repo}
	if len(q.Statuses) > 0 {
		placeholders := make([]string, len(q.Statuses))
		for i, status := range q.Statuses {
			placeholders[i] = "?"
			args = append(args, status)
		}
		predicates = append(predicates, "status in ("+strings.Join(placeholders, ",")+")")
	}
	for _, state := range q.States {
		switch state {
		case "active":
			predicates = append(predicates, "status in ('wip','draft','open')")
		case "wip":
			predicates = append(predicates, "status='wip'")
		case "open":
			predicates = append(predicates, "status in ('draft','open')")
		case "closed":
			predicates = append(predicates, "status in ('closed','merged')")
		case "merged":
			predicates = append(predicates, "status='merged'")
		case "unmerged":
			predicates = append(predicates, "status!='merged'")
		}
	}
	if q.Draft != nil {
		if *q.Draft {
			predicates = append(predicates, "draft=1")
		} else {
			predicates = append(predicates, "draft=0")
		}
	}
	if q.Number > 0 {
		predicates = append(predicates, "number=?")
		args = append(args, q.Number)
	}
	for _, term := range q.Terms {
		predicates = append(predicates, "instr(lower(title),lower(?))>0")
		args = append(args, term)
	}
	for _, person := range []struct{ value, column string }{{q.Author, "author_id"}, {q.Assignee, "assignees"}, {q.Reviewer, "reviewers"}} {
		if person.value == "" {
			continue
		}
		membership := "m.id=w.author_id"
		if person.column != "author_id" {
			membership = "m.id in (select value from json_each(w." + person.column + "))"
		}
		identity := "m.login=? collate nocase"
		if person.value == "@me" {
			identity = "m.is_me=1"
		} else {
			args = append(args, person.value)
		}
		predicates = append(predicates, "exists(select 1 from github_repository_members m where m.repository_id=w.repository_id and "+membership+" and "+identity+")")
	}
	var labels []issues.Label
	if len(q.Labels) > 0 {
		labels, err = readLabels(ctx, tx, repo)
		if err != nil {
			return result, err
		}
	}
	for _, expression := range q.Labels {
		predicate, values := labelPredicate(expression, labels)
		predicates = append(predicates, predicate)
		args = append(args, values...)
	}
	order := map[string]string{"created-desc": timestampOrder("created_at", "desc"), "created-asc": timestampOrder("created_at", "asc"), "updated-desc": timestampOrder("updated_at", "desc"), "updated-asc": timestampOrder("updated_at", "asc")}[q.Sort]
	if err = readPage(ctx, tx, cte, strings.Join(predicates, " and "), args, order, q.Title, &result); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (s *Store) MyWork(ctx context.Context, repo, view string, page, size int) (workitems.Result, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workitems.Result{}, err
	}
	defer tx.Rollback()
	result, err := metadata(ctx, tx, repo, page, size)
	if err != nil {
		return result, err
	}
	result.Sync = sections(result.Sync, "identity", "issues", "pull_requests", "pull_requests_participants", "history_participants")
	result.Counts = map[string]int{"all": 0, "review_requested": 0, "assigned_issues": 0, "assigned_pull_requests": 0}
	if result.Identity == nil {
		return result, tx.Commit()
	}
	me := result.Identity.ID
	cte := `select *, (status in ('draft','open') and kind='pull_request' and ? in (select value from json_each(reviewers))) review_requested,
 (status='open' and kind='issue' and ? in (select value from json_each(assignees))) assigned_issues,
 (status in ('wip','draft','open') and kind='pull_request' and ? in (select value from json_each(assignees))) assigned_pull_requests from (` + prCTE + ` union all ` + issueCTE + `) where repository_id=?`
	args := []any{me, me, me, repo}
	const anyReason = "(review_requested or assigned_issues or assigned_pull_requests)"
	for _, name := range []string{"all", "review_requested", "assigned_issues", "assigned_pull_requests"} {
		predicate := name
		if name == "all" {
			predicate = anyReason
		}
		var count int
		if err = tx.QueryRowContext(ctx, "with w as ("+cte+") select count(*) from w where "+predicate, args...).Scan(&count); err != nil {
			return result, err
		}
		result.Counts[name] = count
	}
	where := view
	if view == "all" {
		where = anyReason
	}
	if err = readPage(ctx, tx, cte, where, args, timestampOrder("updated_at", "desc"), "", &result); err != nil {
		return result, err
	}
	for i := range result.Rows {
		r := &result.Rows[i]
		if r.Kind == "issue" {
			r.Reasons = append(r.Reasons, "Assigned issue")
		} else {
			for _, id := range r.AssigneeIDs {
				if id == me {
					r.Reasons = append(r.Reasons, "Assigned PR")
				}
			}
			if r.Status != "wip" {
				for _, id := range r.ReviewerIDs {
					if id == me {
						r.Reasons = append(r.Reasons, "Review requested")
					}
				}
			}
		}
	}
	return result, tx.Commit()
}
func metadata(ctx context.Context, tx *sql.Tx, repo string, page, size int) (workitems.Result, error) {
	r := workitems.Result{Rows: []workitems.Row{}, Sync: []workitems.SyncStatus{}, Page: page, PerPage: size}
	m := githubidentity.Member{}
	err := tx.QueryRowContext(ctx, `select id,login,coalesce(avatar_url,''),coalesce(profile_url,''),permission,is_me from github_repository_members where repository_id=? and is_me=1`, repo).Scan(&m.ID, &m.Login, &m.AvatarURL, &m.ProfileURL, &m.Permission, &m.IsMe)
	if err == nil {
		r.Identity = &m
	} else if !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	rows, err := tx.QueryContext(ctx, `select section,attempted_at,synced_at,error from work_sync_state where repository_id=? order by section`, repo)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var state workitems.SyncStatus
		if err = rows.Scan(&state.Section, &state.AttemptedAt, &state.SyncedAt, &state.Error); err != nil {
			return r, err
		}
		r.Sync = append(r.Sync, state)
	}
	return r, rows.Err()
}

// timestampOrder normalizes RFC3339 offsets and variable fractional precision.
// Keep whole seconds and fractions separate so SQLite's millisecond date precision
// and floating-point epoch precision do not collapse distinct nanosecond timestamps.
func timestampOrder(column, direction string) string {
	seconds := "unixepoch(substr(" + column + ",1,19) || substr(" + column + ",case when substr(" + column + ",-1)='Z' then -1 else -6 end))"
	fraction := "case when substr(" + column + ",20,1)='.' then cast(substr(" + column + ",20) as real) else 0 end"
	return seconds + " " + direction + "," + fraction + " " + direction + ",id"
}

func readPage(ctx context.Context, tx *sql.Tx, cte, where string, args []any, order, title string, result *workitems.Result) error {
	base := "with w as (" + cte + ") "
	fuzzy := strings.TrimSpace(title) != ""
	if !fuzzy {
		if err := tx.QueryRowContext(ctx, base+"select count(*) from w where "+where, args...).Scan(&result.Total); err != nil {
			return err
		}
	}
	query := base + "select id,kind,title,number,status,author_id,assignees,reviewers,updated_at,labels,linked_pull_request_ids,url from w where " + where + " order by " + order
	if !fuzzy {
		query += " limit ? offset ?"
		args = append(append([]any{}, args...), result.PerPage, (result.Page-1)*result.PerPage)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		r := workitems.Row{Reasons: []string{}}
		var assignees, reviewers, labels, linked string
		if err = rows.Scan(&r.ID, &r.Kind, &r.Title, &r.Number, &r.Status, &r.AuthorID, &assignees, &reviewers, &r.UpdatedAt, &labels, &linked, &r.URL); err != nil {
			return err
		}
		if fuzzy {
			if !workitems.MatchTitle(r.Title, title) {
				continue
			}
			result.Total++
			if result.Total <= (result.Page-1)*result.PerPage || len(result.Rows) >= result.PerPage {
				continue
			}
		}
		if err = json.Unmarshal([]byte(assignees), &r.AssigneeIDs); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(reviewers), &r.ReviewerIDs); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(labels), &r.Labels); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(linked), &r.LinkedPullRequestIDs); err != nil {
			return err
		}
		result.Rows = append(result.Rows, r)
	}
	return rows.Err()
}

func sections(states []workitems.SyncStatus, names ...string) []workitems.SyncStatus {
	result := []workitems.SyncStatus{}
	for _, state := range states {
		for _, name := range names {
			if state.Section == name {
				result = append(result, state)
				break
			}
		}
	}
	return result
}

// EXISTS keeps rows and counts unique even when several labels match a group.
func labelPredicate(e *workitems.LabelExpression, labels []issues.Label) (string, []any) {
	if e.Operator == "" {
		// SQLite NOCASE only folds ASCII. Resolve names against the cached catalog
		// with Unicode case folding, then use the indexed assignment identity.
		ids := []string{}
		for _, label := range labels {
			if strings.EqualFold(label.Name, e.Name) {
				ids = append(ids, label.ID)
			}
		}
		if len(ids) == 0 {
			return "0", nil
		}
		encoded, _ := json.Marshal(ids)
		return `exists(select 1 from issue_label_assignments a where a.issue_id=w.id and a.label_id in (select value from json_each(?)))`, []any{string(encoded)}
	}
	parts := []string{}
	args := []any{}
	for _, child := range e.Children {
		part, values := labelPredicate(child, labels)
		parts = append(parts, part)
		args = append(args, values...)
	}
	return "(" + strings.Join(parts, " "+e.Operator+" ") + ")", args
}

func (s *Store) Labels(ctx context.Context, repo string) ([]issues.Label, error) {
	return readLabels(ctx, s.db, repo)
}

func readLabels(ctx context.Context, reader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, repo string) ([]issues.Label, error) {
	rows, err := reader.QueryContext(ctx, `select id,name,color,description from issue_labels where repository_id=? order by name collate nocase,id`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []issues.Label{}
	for rows.Next() {
		var label issues.Label
		if err := rows.Scan(&label.ID, &label.Name, &label.Color, &label.Description); err != nil {
			return nil, err
		}
		result = append(result, label)
	}
	return result, rows.Err()
}
